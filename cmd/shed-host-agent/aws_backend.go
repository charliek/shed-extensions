package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// AWSBackend performs AWS credential operations on the host.
type AWSBackend interface {
	// GetCredentials returns temporary AWS credentials for the given shed on
	// the given server.
	GetCredentials(ctx context.Context, server, shedName string) (*AWSCachedCredentials, error)

	// Status returns the role and cache expiration for the given shed on the
	// given server.
	Status(server, shedName string) (role string, cachedUntil *time.Time)
}

// AWSCachedCredentials holds a cached set of STS temporary credentials.
type AWSCachedCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expiration      time.Time
}

// stsBackend performs STS AssumeRole using the developer's local AWS profile.
type stsBackend struct {
	client        *sts.Client
	cfg           AWSConfig
	refreshBefore time.Duration
	sessionDur    time.Duration
	logger        *slog.Logger

	mu    sync.Mutex
	cache map[string]*AWSCachedCredentials
}

// NewSTSBackend creates an AWS backend that assumes roles via STS.
func NewSTSBackend(ctx context.Context, cfg AWSConfig, logger *slog.Logger) (AWSBackend, error) {
	if !cfg.HasAnyRole() {
		return nil, fmt.Errorf("no AWS role configured (set aws.default_role, aws.sheds, or aws.servers)")
	}

	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithSharedConfigProfile(cfg.SourceProfile),
	)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config for profile %q: %w", cfg.SourceProfile, err)
	}

	refreshBefore, err := time.ParseDuration(cfg.CacheRefreshBefore)
	if err != nil {
		logger.Warn("invalid cache_refresh_before, using default", "value", cfg.CacheRefreshBefore, "default", "5m")
		refreshBefore = 5 * time.Minute
	}

	sessionDur, err := time.ParseDuration(cfg.SessionDuration)
	if err != nil {
		logger.Warn("invalid session_duration, using default", "value", cfg.SessionDuration, "default", "1h")
		sessionDur = 1 * time.Hour
	}

	logger.Info("AWS backend initialized",
		"profile", cfg.SourceProfile,
		"default_role", cfg.DefaultRole,
		"session_duration", sessionDur,
		"cache_refresh_before", refreshBefore,
	)

	return &stsBackend{
		client:        sts.NewFromConfig(awsCfg),
		cfg:           cfg,
		refreshBefore: refreshBefore,
		sessionDur:    sessionDur,
		logger:        logger,
		cache:         make(map[string]*AWSCachedCredentials),
	}, nil
}

func (b *stsBackend) GetCredentials(ctx context.Context, server, shedName string) (*AWSCachedCredentials, error) {
	resolved := b.cfg.Resolve(server, shedName)
	roleARN := resolved.Role
	if roleARN == "" {
		return nil, fmt.Errorf("no role configured for shed %q on server %q", shedName, server)
	}

	// Cache and session names are keyed by server/shed so identical shed names
	// on different servers don't collide.
	cacheKey := serverShedKey(server, shedName)

	// Check cache under lock
	b.mu.Lock()
	if cached, ok := b.cache[cacheKey]; ok {
		if time.Until(cached.Expiration) > b.refreshBefore {
			b.mu.Unlock()
			b.logger.Debug("returning cached credentials", "server", server, "shed", shedName, "expires", cached.Expiration)
			return cached, nil
		}
	}
	b.mu.Unlock()

	durationSec := int32(b.resolveSessionDur(resolved).Seconds())

	// Assume role without holding the lock (avoids blocking other sheds)
	sessionName := fmt.Sprintf("shed-%s-%s-%d", server, shedName, time.Now().Unix())

	result, err := b.client.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         &roleARN,
		RoleSessionName: &sessionName,
		DurationSeconds: &durationSec,
	})
	if err != nil {
		return nil, fmt.Errorf("sts:AssumeRole failed for %s: %w", roleARN, err)
	}
	if result.Credentials == nil {
		return nil, fmt.Errorf("sts:AssumeRole returned nil credentials for %s", roleARN)
	}

	creds := &AWSCachedCredentials{
		AccessKeyID:     *result.Credentials.AccessKeyId,
		SecretAccessKey: *result.Credentials.SecretAccessKey,
		SessionToken:    *result.Credentials.SessionToken,
		Expiration:      *result.Credentials.Expiration,
	}

	b.mu.Lock()
	b.cache[cacheKey] = creds
	b.mu.Unlock()

	b.logger.Info("assumed role",
		"server", server,
		"shed", shedName,
		"role", roleARN,
		"session", sessionName,
		"expires", creds.Expiration,
	)

	return creds, nil
}

// resolveSessionDur returns the session duration for a resolved policy, falling
// back to the backend default when the override is unset or invalid.
func (b *stsBackend) resolveSessionDur(resolved ResolvedAWS) time.Duration {
	if resolved.SessionDuration == "" {
		return b.sessionDur
	}
	return parseDurationOr(resolved.SessionDuration, b.sessionDur, "session_duration", b.logger)
}

func (b *stsBackend) Status(server, shedName string) (string, *time.Time) {
	role := b.cfg.Resolve(server, shedName).Role

	b.mu.Lock()
	defer b.mu.Unlock()

	if cached, ok := b.cache[serverShedKey(server, shedName)]; ok {
		return role, &cached.Expiration
	}
	return role, nil
}
