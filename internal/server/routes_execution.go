package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountExecutionRoutes(r chi.Router) {
	r.Get("/api/execution/devices", s.listExecutionDevices)
	r.With(s.fullRemoteAccessGuard).Post("/api/execution/devices", s.registerRemoteExecutionDevice)
	r.With(s.fullRemoteAccessGuard).Post("/api/execution/devices/{deviceId}/enable", s.enableExecutionDevice)
	r.With(s.fullRemoteAccessGuard).Post("/api/execution/devices/{deviceId}/disable", s.disableExecutionDevice)
	r.With(s.fullRemoteAccessGuard).Put("/api/projects/{projectId}/execution-devices/{deviceId}", s.setProjectExecutionDeviceGrant)
	r.With(s.fullRemoteAccessGuard).Patch("/api/agents/{id}/execution-device", s.setAgentExecutionDevice)
	r.Get("/api/execution/tasks", s.listRemoteExecutionTasks)
	r.Post("/api/execution/tasks", s.createRemoteExecutionTask)
	r.Get("/api/execution/tasks/{taskId}", s.getRemoteExecutionTask)
}
