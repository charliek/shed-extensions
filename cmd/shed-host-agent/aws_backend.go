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
	// GetCredentials returns temporary AWS credentials for the given shed.
	GetCredentials(ctx context.Context, shedName string) (*AWSCachedCredentials, error)
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
	if cfg.DefaultRole == "" && len(cfg.Sheds) == 0 {
		return nil, fmt.Errorf("no AWS role configured (set aws.default_role or aws.sheds)")
	}

	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithSharedConfigProfile(cfg.SourceProfile),
	)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config for profile %q: %w", cfg.SourceProfile, err)
	}

	refreshBefore, err := time.ParseDuration(cfg.CacheRefreshBefore)
	if err != nil {
		refreshBefore = 5 * time.Minute
	}

	sessionDur, err := time.ParseDuration(cfg.SessionDuration)
	if err != nil {
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

func (b *stsBackend) GetCredentials(ctx context.Context, shedName string) (*AWSCachedCredentials, error) {
	roleARN := b.resolveRole(shedName)
	if roleARN == "" {
		return nil, fmt.Errorf("no role configured for shed %q", shedName)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Check cache
	if cached, ok := b.cache[shedName]; ok {
		if time.Until(cached.Expiration) > b.refreshBefore {
			b.logger.Debug("returning cached credentials", "shed", shedName, "expires", cached.Expiration)
			return cached, nil
		}
	}

	// Assume role
	sessionName := fmt.Sprintf("shed-%s-%d", shedName, time.Now().Unix())
	durationSec := int32(b.sessionDur.Seconds())

	result, err := b.client.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         &roleARN,
		RoleSessionName: &sessionName,
		DurationSeconds: &durationSec,
	})
	if err != nil {
		return nil, fmt.Errorf("sts:AssumeRole failed for %s: %w", roleARN, err)
	}

	creds := &AWSCachedCredentials{
		AccessKeyID:     *result.Credentials.AccessKeyId,
		SecretAccessKey: *result.Credentials.SecretAccessKey,
		SessionToken:    *result.Credentials.SessionToken,
		Expiration:      *result.Credentials.Expiration,
	}

	b.cache[shedName] = creds
	b.logger.Info("assumed role",
		"shed", shedName,
		"role", roleARN,
		"session", sessionName,
		"expires", creds.Expiration,
	)

	return creds, nil
}

func (b *stsBackend) resolveRole(shedName string) string {
	if shedCfg, ok := b.cfg.Sheds[shedName]; ok && shedCfg.Role != "" {
		return shedCfg.Role
	}
	return b.cfg.DefaultRole
}
