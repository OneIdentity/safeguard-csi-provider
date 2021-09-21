package provider

import (
	"golang.org/x/net/context"
	"os"
)

type Provider struct {
}

// NewProvider creates a new provider
func NewProvider() *Provider {
	return &Provider{
	}
}

// MountSecretsStoreObjectContent mounts content of the secrets store object to target path
func (p *Provider) MountSecretsStoreObjectContent(ctx context.Context, attrib map[string]string, secrets map[string]string, targetPath string, permission os.FileMode) (map[string][]byte, map[string]string, error) {
	objectVersionMap := make(map[string]string)
	files := make(map[string][]byte)

	// TODO: Fetch secrets from Safeguard
	return files, objectVersionMap, nil
}
