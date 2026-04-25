package router

import (
	"github.com/gin-gonic/gin"

	"codescan/internal/api/handler"
	"codescan/internal/api/middleware"
)

func InitRouter(r *gin.Engine, authKey string) {
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
			auth.POST("/tasks", handler.UploadHandler)
			auth.DELETE("/tasks/:id", handler.DeleteTaskHandler)
			auth.POST("/tasks/:id/pause", handler.PauseTaskHandler)
			auth.POST("/tasks/:id/resume", handler.ResumeTaskHandler)
			auth.POST("/tasks/:id/orchestration/start", handler.StartOrchestrationHandler)
			auth.POST("/tasks/:id/stage/:stage_name", handler.RunStageHandler)
			auth.POST("/tasks/:id/stage/:stage_name/gap-check", handler.GapCheckStageHandler)
			auth.POST("/tasks/:id/stage/:stage_name/revalidate", handler.RevalidateStageHandler)
			auth.POST("/tasks/:id/repair", handler.RepairJSONHandler)
		}
	}
}
