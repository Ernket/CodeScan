package router

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"codescan/internal/api/handler"
	"codescan/internal/api/middleware"
)

func InitRouter(r *gin.Engine, authKey string) {
	InitRouterWithFrontend(r, authKey, nil)
}

func InitRouterWithFrontend(r *gin.Engine, authKey string, frontend fs.FS) {
	r.Use(middleware.CorsMiddleware())

	api := r.Group("/api")
	{
		api.POST("/login", handler.LoginHandler(authKey))

		auth := api.Group("/")
		auth.Use(middleware.AuthMiddleware(authKey))
		{
			auth.GET("/stats", handler.GetStatsHandler)
			auth.GET("/tasks", handler.GetTasksHandler)
			auth.GET("/tasks/:id", handler.GetTaskDetailHandler)
			auth.GET("/tasks/:id/report", handler.ExportTaskReportHandler)
			auth.GET("/tasks/:id/orchestration", handler.GetOrchestrationSnapshotHandler)
			auth.GET("/tasks/:id/orchestration/subtasks", handler.GetOrchestrationSubtasksHandler)
			auth.GET("/tasks/:id/orchestration/agents", handler.GetOrchestrationAgentsHandler)
			auth.GET("/tasks/:id/orchestration/events", handler.OrchestrationEventsHandler)
			auth.GET("/organizations/accessible", handler.GetAccessibleOrganizationsHandler)
			auth.POST("/tasks", handler.UploadHandler)
			auth.POST("/tasks/:id/pause", handler.PauseTaskHandler)
			auth.POST("/tasks/:id/resume", handler.ResumeTaskHandler)
			auth.POST("/tasks/:id/orchestration/start", handler.StartOrchestrationHandler)
			auth.POST("/tasks/:id/orchestration/rerun", handler.RerunOrchestrationHandler)
			auth.POST("/tasks/:id/stage/:stage_name", handler.RunStageHandler)
			auth.POST("/tasks/:id/stage/:stage_name/gap-check", handler.GapCheckStageHandler)
			auth.POST("/tasks/:id/stage/:stage_name/revalidate", handler.RevalidateStageHandler)
			auth.POST("/tasks/:id/repair", handler.RepairJSONHandler)

			orgGroup := auth.Group("/organizations")
			orgGroup.Use(middleware.RequireUserManagement())
			{
				orgGroup.GET("", handler.ListOrganizationsHandler)
				orgGroup.POST("", handler.CreateOrganizationHandler)
				orgGroup.PATCH("/:id", handler.UpdateOrganizationHandler)
				orgGroup.DELETE("/:id", handler.DeleteOrganizationHandler)
			}

			deleteGroup := auth.Group("/")
			deleteGroup.Use(middleware.RequireDelete())
			{
				deleteGroup.DELETE("/tasks/:id", handler.DeleteTaskHandler)
			}

			userGroup := auth.Group("/users")
			userGroup.Use(middleware.RequireUserManagement())
			{
				userGroup.GET("", handler.ListUsersHandler)
				userGroup.POST("", handler.CreateUserHandler)
				userGroup.PATCH("/:id/status", handler.UpdateUserStatusHandler)
				userGroup.POST("/:id/password", handler.ResetUserPasswordHandler)
				userGroup.PUT("/:id/organizations", handler.ReplaceUserOrganizationsHandler)
			}
		}
	}

	if frontend != nil {
		r.NoRoute(frontendHandler(frontend))
	}
}

func frontendHandler(frontend fs.FS) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestPath := c.Request.URL.Path
		if isAPIPath(requestPath) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "Not found"})
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		filePath := cleanFrontendPath(requestPath)
		if filePath == "" {
			serveFrontendFile(c, frontend, "index.html")
			return
		}
		if frontendFileExists(frontend, filePath) {
			serveFrontendFile(c, frontend, filePath)
			return
		}
		if path.Ext(filePath) != "" {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		serveFrontendFile(c, frontend, "index.html")
	}
}

func isAPIPath(requestPath string) bool {
	return requestPath == "/api" || strings.HasPrefix(requestPath, "/api/")
}

func cleanFrontendPath(requestPath string) string {
	cleaned := path.Clean("/" + requestPath)
	if cleaned == "/" {
		return ""
	}
	return strings.TrimPrefix(cleaned, "/")
}

func frontendFileExists(frontend fs.FS, filePath string) bool {
	info, err := fs.Stat(frontend, filePath)
	return err == nil && !info.IsDir()
}

func serveFrontendFile(c *gin.Context, frontend fs.FS, filePath string) {
	data, err := fs.ReadFile(frontend, filePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	contentType := mime.TypeByExtension(path.Ext(filePath))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if filePath == "index.html" {
		c.Header("Cache-Control", "no-cache")
	}
	c.Data(http.StatusOK, contentType, data)
}
