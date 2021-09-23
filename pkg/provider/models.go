package provider

import (
	"google.golang.org/genproto/googleapis/type/datetime"
)

type A2ARegistration struct {
	Id                        int
	AppName                   string
	Description               string
	Disabled                  bool
	VisibleToCertificateUsers bool
	CertificateUserId         int
	CertificateUserThumbPrint string
	CertificateUser           string
	CreatedDate               datetime.DateTime
	CreatedByUserId           int
	CreatedByUserDisplayName  string
}

type RetrievableAccount struct {
	AccountId          int
	AccountName        string
	ApiKey             string
	IpRestrictions     string
	SystemId           int
	SystemName         string
	SystemDescription  string
	AccountDescription string
	NetworkAddress     string
	AccountDisabled    int
	AccountType        string
	DomainName         string
}

type OAuth2AccessToken struct {
	AccessToken		   string `json:"access_token"`
	ExpiresIn		   int    `json:"expires_in"`
	Scope		       string
	Success            bool
	TokenType          string `json:"token_type"`
}
