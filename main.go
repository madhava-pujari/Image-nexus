package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/VinayakBagaria/photogram/api/resthandlers"
	"github.com/VinayakBagaria/photogram/api/routes"
	"github.com/VinayakBagaria/photogram/config"
	"github.com/VinayakBagaria/photogram/db"
	"github.com/VinayakBagaria/photogram/docs"
	"github.com/VinayakBagaria/photogram/service"
	"github.com/VinayakBagaria/photogram/storage"
	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

func main() {
	err := config.Init("config", "./")
	if err != nil {
		log.Fatalln("Unable to read the config file: %w", err)
	}

	router := gin.Default()
	// Logger middleware will write the logs to gin.DefaultWriter = os.Stdout
	router.Use(gin.Logger())
	// Recovery middleware recovers from any panics and writes a 500 if there was one.
	router.Use(gin.Recovery())
	router.MaxMultipartMemory = 8 << 20 // 8 MiB

	// Set swagger data
	docs.SwaggerInfo.Title = "Cat Pictures"
	docs.SwaggerInfo.Description = "RESTful API for uploading and managing cat pictures."
	docs.SwaggerInfo.Schemes = []string{"http"}

	// Initialize the db
	dbConfig := db.NewConfiguration()
	dbHandler, err := db.NewConnection(dbConfig)
	if err != nil {
		log.Panicln(err)
	}

	repository := db.NewPicturesRepository(dbHandler)
	localStorage := storage.NewStorage(config.GetConfigValue("server.imagePath"))
	service := service.NewPicturesService(repository, localStorage)
	handler := resthandlers.NewPicturesHandler(service)
	routesList := routes.NewPicturesRoutes(handler)

	serverHandler := resthandlers.NewServerHandler()
	serverRoutesList := routes.NewServerRouteList(serverHandler)

	routes.Install(router, routesList)
	routes.Install(router, serverRoutesList)
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	apiPort, err := strconv.Atoi(config.GetConfigValue("server.port"))
	if err != nil {
		log.Fatalln("Unable to parse api port")
	}

	log.Printf("API service running on port: %d", apiPort)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", apiPort), router))
}
