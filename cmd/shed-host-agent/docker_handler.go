package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	sdk "github.com/charliek/shed/sdk"

	"github.com/charliek/shed-extensions/internal/protocol"
)

// DockerHandler processes Docker credential requests from the plugin message bus.
type DockerHandler struct {
	backend DockerBackend
	client  *sdk.HostClient
	audit   *AuditLogger
	logger  *slog.Logger
}

// NewDockerHandler creates a handler for the docker-credentials namespace.
func NewDockerHandler(backend DockerBackend, client *sdk.HostClient, audit *AuditLogger, logger *slog.Logger) *DockerHandler {
	return &DockerHandler{
		backend: backend,
		client:  client,
		audit:   audit,
		logger:  logger,
	}
}

// Run subscribes to the docker-credentials namespace and processes messages until
// the context is cancelled.
func (h *DockerHandler) Run(ctx context.Context) {
	ch := h.client.Subscribe(ctx, protocol.NamespaceDockerCredentials)

	for env := range ch {
		h.handleMessage(ctx, env)
	}
}

func (h *DockerHandler) handleMessage(ctx context.Context, env *sdk.Envelope) {
	shedName := ""
	if env.Shed != nil {
		shedName = env.Shed.Name
	}

	var op struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(env.Payload, &op); err != nil {
		h.logger.Error("failed to parse operation", "error", err)
		h.sendError(ctx, env, "invalid payload", protocol.DockerCodeInternal)
		return
	}

	switch op.Operation {
	case protocol.DockerOpGet:
		h.handleGet(ctx, env, shedName)
	case protocol.DockerOpList:
		h.handleList(ctx, env, shedName)
	case protocol.DockerOpPing:
		h.handlePing(ctx, env, shedName)
	case protocol.DockerOpStatus:
		h.handleStatus(ctx, env, shedName)
	default:
		h.logger.Warn("unknown operation", "operation", op.Operation, "shed", shedName)
		h.sendError(ctx, env, fmt.Sprintf("unknown operation: %s", op.Operation), protocol.DockerCodeInternal)
	}
}

func (h *DockerHandler) handleGet(ctx context.Context, env *sdk.Envelope, shedName string) {
	var req protocol.DockerGetRequest
	if err := json.Unmarshal(env.Payload, &req); err != nil {
		h.logger.Error("failed to parse get request", "error", err)
		h.sendError(ctx, env, "invalid get request", protocol.DockerCodeInternal)
		return
	}

	cred, err := h.backend.GetCredentials(ctx, req.ServerURL)
	if err != nil {
		h.logger.Error("get credentials failed", "error", err, "shed", shedName, "registry", req.ServerURL)

		code := protocol.DockerCodeInternal
		if de, ok := err.(*dockerError); ok {
			code = de.code
		}
		// Send a generic message to the guest; the full error is logged host-side above
		h.sendError(ctx, env, "credential request failed", code)
		h.audit.Log(shedName, protocol.NamespaceDockerCredentials, protocol.DockerOpGet, "error", req.ServerURL, "none")
		return
	}

	resp := protocol.DockerGetResponse{
		ServerURL: cred.ServerURL,
		Username:  cred.Username,
		Secret:    cred.Secret,
	}

	h.sendResponse(ctx, env, resp)
	h.audit.Log(shedName, protocol.NamespaceDockerCredentials, protocol.DockerOpGet, "ok", req.ServerURL, "none")
	h.logger.Debug("credentials served", "shed", shedName, "registry", req.ServerURL)
}

func (h *DockerHandler) handleList(ctx context.Context, env *sdk.Envelope, shedName string) {
	registries, err := h.backend.ListCredentials(ctx)
	if err != nil {
		h.logger.Error("list credentials failed", "error", err, "shed", shedName)
		h.sendError(ctx, env, "list failed", protocol.DockerCodeInternal)
		return
	}

	resp := protocol.DockerListResponse{Registries: registries}
	h.sendResponse(ctx, env, resp)
	h.audit.Log(shedName, protocol.NamespaceDockerCredentials, protocol.DockerOpList, "ok", fmt.Sprintf("count:%d", len(registries)), "none")
	h.logger.Debug("list", "shed", shedName, "count", len(registries))
}

func (h *DockerHandler) handlePing(ctx context.Context, env *sdk.Envelope, shedName string) {
	resp := protocol.DockerPingResponse{Status: "ok"}
	h.sendResponse(ctx, env, resp)
	h.logger.Debug("ping", "shed", shedName)
}

func (h *DockerHandler) handleStatus(ctx context.Context, env *sdk.Envelope, shedName string) {
	allowAll, registryCount := h.backend.Status()

	resp := protocol.DockerStatusResponse{
		Connected:     true,
		AllowAll:      allowAll,
		RegistryCount: registryCount,
	}

	h.sendResponse(ctx, env, resp)
	h.logger.Debug("status", "shed", shedName, "allow_all", allowAll, "registry_count", registryCount)
}

func (h *DockerHandler) sendResponse(ctx context.Context, req *sdk.Envelope, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		h.logger.Error("failed to marshal response", "error", err)
		return
	}

	resp := sdk.NewResponse(req.ID, req.Namespace, data)
	resp.Shed = req.Shed

	if err := h.client.Respond(ctx, req.Namespace, resp); err != nil {
		h.logger.Error("failed to send response", "error", err)
	}
}

func (h *DockerHandler) sendError(ctx context.Context, req *sdk.Envelope, msg, code string) {
	errResp := protocol.DockerErrorResponse{Error: msg, Code: code}
	h.sendResponse(ctx, req, errResp)
}
