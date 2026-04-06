package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	sdk "github.com/charliek/shed/sdk"

	"github.com/charliek/shed-extensions/internal/protocol"
)

// AWSHandler processes AWS credential requests from the plugin message bus.
type AWSHandler struct {
	backend AWSBackend
	client  *sdk.HostClient
	audit   *AuditLogger
	logger  *slog.Logger
}

// NewAWSHandler creates a handler for the aws-credentials namespace.
func NewAWSHandler(backend AWSBackend, client *sdk.HostClient, audit *AuditLogger, logger *slog.Logger) *AWSHandler {
	return &AWSHandler{
		backend: backend,
		client:  client,
		audit:   audit,
		logger:  logger,
	}
}

// Run subscribes to the aws-credentials namespace and processes messages until
// the context is cancelled.
func (h *AWSHandler) Run(ctx context.Context) {
	ch := h.client.Subscribe(ctx, protocol.NamespaceAWSCredentials)

	for env := range ch {
		h.handleMessage(ctx, env)
	}
}

func (h *AWSHandler) handleMessage(ctx context.Context, env *sdk.Envelope) {
	shedName := ""
	if env.Shed != nil {
		shedName = env.Shed.Name
	}

	var op struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(env.Payload, &op); err != nil {
		h.logger.Error("failed to parse operation", "error", err)
		h.sendError(ctx, env, "invalid payload", protocol.AWSCodeInternal)
		return
	}

	switch op.Operation {
	case protocol.AWSOpGetCredentials:
		h.handleGetCredentials(ctx, env, shedName)
	case protocol.AWSOpPing:
		h.handlePing(ctx, env, shedName)
	case protocol.AWSOpStatus:
		h.handleStatus(ctx, env, shedName)
	default:
		h.logger.Warn("unknown operation", "operation", op.Operation, "shed", shedName)
		h.sendError(ctx, env, fmt.Sprintf("unknown operation: %s", op.Operation), protocol.AWSCodeInternal)
	}
}

func (h *AWSHandler) handleGetCredentials(ctx context.Context, env *sdk.Envelope, shedName string) {
	creds, err := h.backend.GetCredentials(ctx, shedName)
	if err != nil {
		h.logger.Error("get credentials failed", "error", err, "shed", shedName)
		h.sendError(ctx, env, "credential request failed", protocol.AWSCodeAssumeRoleFailed)
		h.audit.Log(shedName, protocol.NamespaceAWSCredentials, protocol.AWSOpGetCredentials, "error", err.Error(), "none")
		return
	}

	resp := protocol.AWSCredentialsResponse{
		AccessKeyID:     creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey,
		SessionToken:    creds.SessionToken,
		Expiration:      creds.Expiration.Format("2006-01-02T15:04:05Z"),
	}

	h.sendResponse(ctx, env, resp)
	h.audit.Log(shedName, protocol.NamespaceAWSCredentials, protocol.AWSOpGetCredentials, "ok", fmt.Sprintf("expires:%s", creds.Expiration.Format("15:04")), "none")
	h.logger.Debug("credentials served", "shed", shedName, "expires", creds.Expiration)
}

func (h *AWSHandler) handlePing(ctx context.Context, env *sdk.Envelope, shedName string) {
	resp := protocol.AWSPingResponse{Status: "ok"}
	h.sendResponse(ctx, env, resp)
	h.logger.Debug("ping", "shed", shedName)
}

func (h *AWSHandler) handleStatus(ctx context.Context, env *sdk.Envelope, shedName string) {
	role, cachedUntil := h.backend.Status(shedName)

	resp := protocol.AWSStatusResponse{
		Connected: true,
		Role:      role,
	}
	if cachedUntil != nil {
		resp.CachedUntil = cachedUntil.Format("2006-01-02T15:04:05Z")
	}

	h.sendResponse(ctx, env, resp)
	h.logger.Debug("status", "role", role, "shed", shedName)
}

func (h *AWSHandler) sendResponse(ctx context.Context, req *sdk.Envelope, payload any) {
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

func (h *AWSHandler) sendError(ctx context.Context, req *sdk.Envelope, msg, code string) {
	errResp := protocol.AWSErrorResponse{Error: msg, Code: code}
	h.sendResponse(ctx, req, errResp)
}
