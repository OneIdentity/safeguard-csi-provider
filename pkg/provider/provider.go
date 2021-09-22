package provider

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
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

	sgHost := strings.TrimSpace(attrib["safeguardHost"])
	appName := strings.TrimSpace(attrib["appName"])
	podName := strings.TrimSpace(attrib["csi.storage.k8s.io/pod.name"])
	podNamespace := strings.TrimSpace(attrib["csi.storage.k8s.io/pod.namespace"])

	var clientCertificate, clientKey []byte

	for k, v := range secrets {
		switch k {
		case "clientCertificate":
			clientCertificate = []byte(v)
		case "clientKey":
			clientKey = []byte(v)
		}
	}

	cert, err := tls.X509KeyPair(clientCertificate, clientKey)
	if err != nil {
		klog.Error(err)
		return files, objectVersionMap, err
	}

	// Get bearer token
	accessToken, err := p.GetToken(sgHost, cert)
	if err != nil {
		klog.Error(err)
		return files, objectVersionMap, err
	}

	// Get the A2A registrations for this client certificate
	registrations, err := p.GetA2aRegistrations(sgHost, accessToken, cert)
	if err != nil {
		klog.Error(err)
		return files, objectVersionMap, err
	}

	if len(registrations) == 0 {
		klog.Error("No app registrations were found")
		return files, objectVersionMap, fmt.Errorf("no app registrations were round")
	}

	appRegistration := Find(registrations, appName)
	if appRegistration == nil {
		klog.Errorf("Requested app name %s was not found", appName)
		return files, objectVersionMap, fmt.Errorf("requested app name %s was not found", appName)
	}

	// Get retrievable accounts for this registration
	accounts, err := p.GetRetrievableAccounts(sgHost, appRegistration, accessToken, cert)
	if err != nil {
		klog.Error(err)
		return files, objectVersionMap, err
	}

	if len(accounts) == 0 {
		klog.Warning("No accounts were found")
		return files, objectVersionMap, err
	}

	// Get each account credential and map into data being returned to driver
	for _, account := range accounts {
		klog.Infof("Looking up %s", account.AccountName)

		cred, err := p.GetCredential(sgHost, account, cert)
		if err != nil {
			klog.Errorf("Could not fetch secret %s because %s", account.AccountName, err.Error())
			continue
		}

		// TODO: We should figure out how to grab a proper version
		objectVersionMap[strconv.Itoa(account.AccountId)] = uuid.New().String()
		files[account.AccountName] = cred

		klog.InfoS("added file to the gRPC response", "file", account.AccountName, "pod", klog.ObjectRef{Namespace: podNamespace, Name: podName})
	}

	return files, objectVersionMap, nil
}

func (p *Provider) GetA2aRegistrations(sgHost string, accessToken string, cert tls.Certificate) ([]*A2ARegistration, error) {
	resp, err := p.SendGetRequest(sgHost, "/service/core/v3/A2ARegistrations", nil, cert, &accessToken, nil)
	if err != nil {
		klog.Error(err)
		return nil, err
	} else if resp.StatusCode != http.StatusOK {
		klog.Errorf("Request failed with status code %d", resp.StatusCode)
		return nil, fmt.Errorf("request failed with status code %d", resp.StatusCode)
	}

	defer resp.Body.Close()
	// TODO: Error handling?
	var registrations []*A2ARegistration
	json.NewDecoder(resp.Body).Decode(&registrations)
	return registrations, nil
}

func (p *Provider) GetRetrievableAccounts(sgHost string, reg *A2ARegistration, accessToken string, cert tls.Certificate) ([]*RetrievableAccount, error) {
	resp, err := p.SendGetRequest(sgHost, fmt.Sprintf("/service/core/v3/A2ARegistrations/%s/RetrievableAccounts", strconv.Itoa(reg.Id)), nil, cert, &accessToken, nil)
	if err != nil {
		klog.Error(err)
		return nil, err
	} else if resp.StatusCode != http.StatusOK {
		klog.Errorf("Request failed with status code %d", resp.StatusCode)
		return nil, fmt.Errorf("request failed with status code %d", resp.StatusCode)
	}

	defer resp.Body.Close()
	// TODO: Error handling?
	var accounts []*RetrievableAccount
	json.NewDecoder(resp.Body).Decode(&accounts)
	return accounts, nil
}

func (p *Provider) GetCredential(sgHost string, account *RetrievableAccount, cert tls.Certificate) ([]byte, error) {
	params := make(map[string]string)
	params["type"] = "Password"

	resp, err := p.SendGetRequest(sgHost, "/service/a2a/v3/Credentials", params, cert, nil, &account.ApiKey)
	if err != nil {
		klog.Error(err)
		return nil, err
	} else if resp.StatusCode != http.StatusOK {
		klog.Errorf("Request failed with status code %d", resp.StatusCode)
		return nil, fmt.Errorf("request failed with status code %d", resp.StatusCode)
	}

	defer resp.Body.Close()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		klog.Error(err)
		return nil, err
	}
	return bodyBytes, nil
}

func (p *Provider) SendGetRequest(sgHost string, path string, params map[string]string, cert tls.Certificate, accessToken *string, apiKey *string) (*http.Response, error){
	url, err := url.Parse(sgHost)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	url.Path = path

	if params != nil {
		for key, value := range params {
			url.Query().Set(key, value)
		}
	}

	req, err := http.NewRequest("GET", url.String(), nil)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	if accessToken != nil {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", *accessToken))
	} else if apiKey != nil {
		req.Header.Set("Authorization", fmt.Sprintf("A2A %s", *apiKey))

	}

	resp, err := p.SendRequest(req, cert)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	return resp, err
}

func (p *Provider) GetToken(sgHost string, cert tls.Certificate) (string, error){
	url, err := url.Parse(sgHost)
	if err != nil {
		klog.Error(err)
		return "", err
	}

	url.Path = "/RSTS/oauth2/token"
	values := map[string]string{"grant_type": "client_credentials", "scope": "rsts:sts:primaryproviderid:certificate"}
	jsonData, err := json.Marshal(values)

	req, err := http.NewRequest("POST", url.String(), bytes.NewBuffer(jsonData))
	if err != nil {
		klog.Error(err)
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.SendRequest(req, cert)
	if err != nil {
		klog.Error(err)
		return "", err
	}

	defer resp.Body.Close()
	tr := p.DeserializeJsonToMap(resp)

	if val, ok := tr["access_token"]; ok {
		return fmt.Sprintf("%v", val), nil
	}

	return "", errors.New("access_token didn't exist in returned payload")
}

func (p *Provider) DeserializeJsonToMap(resp *http.Response) map[string]interface{} {
	// TODO: Error handling?
	var res map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&res)
	klog.Info(res)

	return res
}

func (p *Provider) GetTlsTransport(cert tls.Certificate) *http.Transport {
	tlsConfig := &tls.Config{
		Renegotiation: tls.RenegotiateFreelyAsClient,
		Certificates: []tls.Certificate{cert},
	}

	return &http.Transport{TLSClientConfig: tlsConfig}
}

func (p *Provider) SendRequest(req *http.Request, cert tls.Certificate) (*http.Response, error){
	transport := p.GetTlsTransport(cert)
	client := &http.Client{Transport: transport}
	return client.Do(req)
}