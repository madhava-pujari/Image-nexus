package storage

import (
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/ioutil"
	"context"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"bytes"

	"imagenexus/dto"
	"imagenexus/utils"

	"github.com/gin-gonic/gin"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/spf13/viper"
)

var CONTENT_DECODERS = map[string](func(r io.Reader) (image.Config, error)){
	"image/jpeg": jpeg.DecodeConfig,
	"image/png":  png.DecodeConfig,
	"image/gif":  gif.DecodeConfig,
	"image/tiff": tiff.DecodeConfig,
	"image/webp": webp.DecodeConfig,
	"image/bmp":  bmp.DecodeConfig,
}

type ImageStorage interface {
	GetFullPath(string) string
	Save(*multipart.FileHeader) (*dto.PictureRequest, *dto.InvalidPictureFileError)
	Get(string) ([]byte, error)
}

type localImageStorage struct {
	path string
}

func NewStorage(path string) ImageStorage {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		err := os.Mkdir(path, os.ModePerm)
		if err != nil {
			log.Fatalln("Unable to make directory: %w", path)
		}
	}

	return &localImageStorage{path}
}

func (s *localImageStorage) GetFullPath(destination string) string {
	return s.path + "/" + destination
}

func (s *localImageStorage) Save(file *multipart.FileHeader) (*dto.PictureRequest, *dto.InvalidPictureFileError) {
	extension := filepath.Ext(file.Filename)
	destination := utils.NewUniqueString() + extension
	fullPath := s.GetFullPath(destination)

	src, err := file.Open()
	if err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      err,
		}
	}
	defer src.Close()

	buffer := make([]byte, 512)
	_, err = src.Read(buffer)
	if err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      err,
		}
	}

	fileType := http.DetectContentType(buffer)
	decoder, ok := CONTENT_DECODERS[fileType]
	if !ok {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New("unsupported format"),
			Data:       gin.H{"format": fileType},
		}
	}

	_, err = src.Seek(0, io.SeekStart)
	if err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      err,
		}
	}

	imageConfig, err := decoder(src)
	if err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      err,
			Data:       gin.H{"format": fileType},
		}
	}

	out, err := os.Create(fullPath)
	if err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      err,
		}
	}
	defer out.Close()

	src.Seek(0, io.SeekStart)
	_, err = io.Copy(out, src)
	if err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      err,
		}
	}

	pictureFile := &dto.PictureRequest{
		Name:        file.Filename,
		Destination: destination,
		Height:      int32(imageConfig.Height),
		Width:       int32(imageConfig.Width),
		Size:        int32(file.Size),
		ContentType: fileType,
	}

	return pictureFile, nil
}

func (s *localImageStorage) Get(destination string) ([]byte, error) {
	fullPath := s.GetFullPath(destination)
	file, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	body, err := ioutil.ReadAll(file)
	return body, err
}




const (
	// viper keys in your config.toml
	cfgS3Bucket       = "storage.s3.bucket"
	cfgS3Prefix       = "storage.s3.prefix"
	cfgCloudFrontURL  = "storage.s3.cloudfront_url"
)

// s3ImageStorage implements ImageStorage, uploading into S3 + serving via CloudFront
type s3ImageStorage struct {
	client       *s3.Client
	uploader     *manager.Uploader
	bucket       string
	prefix       string
	cloudFrontURL string
}

// NewS3Storage reads config via Viper and returns an ImageStorage
func NewS3Storage() (ImageStorage, error) {
	// load AWS creds / region from env / ~/.aws via default chain
	awsCfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	s3Client := s3.NewFromConfig(awsCfg)
	uploader := manager.NewUploader(s3Client)

	bucket := viper.GetString(cfgS3Bucket)
	prefix := viper.GetString(cfgS3Prefix)
	if prefix != "" && prefix[len(prefix)-1] != '/' {
		prefix = prefix + "/"
	}
	cfURL := viper.GetString(cfgCloudFrontURL)

	return &s3ImageStorage{
		client:        s3Client,
		uploader:      uploader,
		bucket:        bucket,
		prefix:        prefix,
		cloudFrontURL: cfURL,
	}, nil
}

// GetFullPath returns the public URL (via CloudFront) for a given object key.
func (s *s3ImageStorage) GetFullPath(destination string) string {
	return fmt.Sprintf("%s/%s%s", s.cloudFrontURL, s.prefix, destination)
}

// Save uploads the file to S3 under prefix + unique name.
// On success it returns a dto.PictureRequest (Destination is the S3 key basename).
func (s *s3ImageStorage) Save(file *multipart.FileHeader) (*dto.PictureRequest, *dto.InvalidPictureFileError) {
	extension := filepath.Ext(file.Filename)
	destination := utils.NewUniqueString() + extension

	src, err := file.Open()
	if err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("cannot open file: %w", err),
		}
	}
	defer src.Close()

	buf := make([]byte, 512)
	if _, err := src.Read(buf); err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("cannot read file header: %w", err),
		}
	}

	contentType := http.DetectContentType(buf)
	decoder, ok := CONTENT_DECODERS[contentType]
	if !ok {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New("unsupported image format"),
			Data:       gin.H{"format": contentType},
		}
	}

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("seek error: %w", err),
		}
	}

	imageCfg, err := decoder(src)
	if err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("decode error: %w", err),
			Data:       gin.H{"format": contentType},
		}
	}

	key := s.prefix + destination
	// reset reader
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("seek before upload: %w", err),
		}
	}

	_, err = s.uploader.Upload(context.TODO(), &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        src,
		ContentType: &contentType,
		ACL:         s3types.ObjectCannedACLPrivate,
	})
	if err != nil {
		return nil, &dto.InvalidPictureFileError{
			StatusCode: http.StatusInternalServerError,
			Error:      fmt.Errorf("s3 upload failed: %w", err),
		}
	}

	pic := &dto.PictureRequest{
		Name:        file.Filename,
		Destination: destination,
		Height:      int32(imageCfg.Height),
		Width:       int32(imageCfg.Width),
		Size:        int32(file.Size),
		ContentType: contentType,
	}
	return pic, nil
}

type S3NotFoundError struct {
	Key string
}
func (e *S3NotFoundError) Error() string {
	return fmt.Sprintf("s3 object %q not found", e.Key)
}

type S3DownloadError struct {
	Key string
	Err error
}
func (e *S3DownloadError) Error() string {
	return fmt.Sprintf("failed to download %q: %v", e.Key, e.Err)
}

func (s *s3ImageStorage) Get(destination string) ([]byte, error) {
	key := s.prefix + destination

	resp, err := s.client.GetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	if err != nil {
		var apiErr interface{ ErrorCode() string }
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchKey" {
			return nil, &S3NotFoundError{Key: destination}
		}
		return nil, &S3DownloadError{Key: destination, Err: err}
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		return nil, &S3DownloadError{Key: destination, Err: err}
	}
	return buf.Bytes(), nil
}
