// Package identity adapts Studio resource authority to central identity.
package identity

import (
	"aster/contracts/appidentity"
	"fmt"
)

const AccessEntitlement = "volumes_studio.access"
const CreateEntitlement = "volumes_studio.create"

var ErrUnavailable = appidentity.ErrUnavailable
var ErrUnauthorized = appidentity.ErrUnauthorized

type Config = appidentity.Config
type Assertion = appidentity.Assertion
type Client struct {
	*appidentity.Client
	config Config
}

func NewClient(config Config) (*Client, error) {
	if config.ClientID != "studio" {
		return nil, fmt.Errorf("Studio identity client required")
	}
	client, err := appidentity.NewClient(config)
	if err != nil {
		return nil, err
	}
	return &Client{Client: client, config: config}, nil
}
func LocalReturn(value string) (string, error) {
	return appidentity.LocalReturn("/volumes-studio/", value)
}
