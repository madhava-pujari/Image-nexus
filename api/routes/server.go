package routes

import (
	"net/http"

	"imagenexus/api/resthandlers"
)

func NewServerRouteList(handlers resthandlers.ServerHandler) []*Route {
	return []*Route{
		{Path: "/healthcheck", Method: http.MethodGet, Handler: handlers.HealthCheck},
	}
}
