package provider

import (
	"bytes"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"golang.org/x/net/context"
	"io/ioutil"
	"log"
	"net/http"
	"os"
)

var (
	certFile = flag.String("cert", "C:\\temp\\certs\\example.crt", "A PEM encoded certificate file.")
	keyFile  = flag.String("key", "C:\\temp\\certs\\example.key", "A PEM encoded private key file.")
	caFile = flag.String("CA", "C:\\temp\\certs\\example.crt", "A PEM encoded CA's certificate file.")
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

	flag.Parse()



	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Fatal(err)
	}

	// Load CA cert
	caCert, err := ioutil.ReadFile(*caFile)
	if err != nil {
		log.Fatal(err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	// Setup HTTPS client
	//http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	//http.DefaultTransport.(*http.Transport).TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
		InsecureSkipVerify: true,
	}
	tlsConfig.BuildNameToCertificate()
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport}

	values := map[string]string{"grant_type": "client_credentials", "scope": "rsts:sts:primaryproviderid:certificate"}
	json_data, err := json.Marshal(values)

	resp, err := client.Post("https://sts-dev.cloud.oneidentity.com/auth/realms/StarlingClients/protocol/openid-connect/token", "application/json", bytes.NewBuffer(json_data))
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	// Dump response
	//data, err := ioutil.ReadAll(resp.Body)
	//if err != nil {
	//	log.Fatal(err)
	//}
	//log.Println(string(data))

	var res map[string]interface{}

	json.NewDecoder(resp.Body).Decode(&res)

	log.Println(res["access_token"])



// below starts to look at getting the registered assets....


	url := "https://customer-asrtest18.westus.cloudapp.azure.com/service/core/v3/A2ARegistrations"

	// Create a Bearer, we need the certificates to work to get a token
	var bearer = "Bearer eyJ0eXAiOiJKV1QiLCJhbGciOiJSUzI1NiIsIng1dCI6ImVJekpha3RRb0ZsQkQ1SXFBUzYxUDdGb2J5byJ9.eyJpc3MiOiJodHRwczovLzc4OENDOTZBNEI1MEEwNTk0MTBGOTIyQTAxMkVCNTNGQjE2ODZGMkEiLCJuYmYiOjE2MzIzMTMxMjEsImV4cCI6MTYzMjMxMzQyMSwiYXV0aG1ldGhvZCI6ImNlcnRpZmljYXRlOmNlcnQiLCJhdXRoX3RpbWUiOiIyMDIxLTA5LTIyVDEyOjE4OjQxLjUyNjA3NzRaIiwiaHR0cDovL3NjaGVtYXMubWljcm9zb2Z0LmNvbS9hY2Nlc3Njb250cm9sc2VydmljZS8yMDEwLzA3L2NsYWltcy9pZGVudGl0eXByb3ZpZGVyIjoiaHR0cHM6Ly83ODhDQzk2QTRCNTBBMDU5NDEwRjkyMkEwMTJFQjUzRkIxNjg2RjJBIiwidXJuOnJzdHMvanRpIjoiMDdmMzA4M2MxYTNhNDczMjk2YmNjNjE5ZWEwMjliOTUiLCJuYW1laWQiOiJDQUU2MUMzNTkxMjdEMEJBNDI0MzQ1RDc5MDE5QzdFOEM3ODhFNjJGIiwidXBuIjoiazhzLXN2YyIsImVtYWlsIjoiIiwidW5pcXVlX25hbWUiOiIiLCJ1cm46cnN0cy9kYXlzVW50aWxQYXNzd29yZEV4cGlyZXMiOiIxMDY3NTE5OS4xMTY3MzAxIiwicnN0czpzdHM6Y2xhaW1zOnVzZXI6dXNlcklkIjoiNCIsInN1YiI6IkNBRTYxQzM1OTEyN0QwQkE0MjQzNDVENzkwMTlDN0U4Qzc4OEU2MkYiLCJhenAiOiIwMDAwMDAwMC0wMDAwLTAwMDAtMDAwMC0wMDAwMDAwMDAwMDAiLCJyc3RzOnN0czpjbGFpbXM6c2NvcGUiOiJyc3RzOnN0czpwcmltYXJ5cHJvdmlkZXJpZDpjZXJ0aWZpY2F0ZTpjZXJ0IiwicnN0czpzdHM6Y2xhaW1zOnNudG5sIjoiMCJ9.YkY929jTuYxkKBuFwK90Ae_6yO3Ni4RLOjt3g5q3fsQF2k14e1J4k01sqesdVORLXaeXUXOXdUJnMejgcBzOOHFycsaP09PrVTkoPGKVQFPJxSTDDWC7_X_aJBT4A4Y932ogQTYyyMNPnEM5oFB_JCHhIF15zLO7Wxmhyq6NmulK529A9ggPe2x96JP8KrkZPRQlGNrU2f-s6w0xPd5z3OVSjY9miXc7vPKb4ZQVHMm9hTwyx3orlUMEKTpj3StBvN8n4fmrGhuOYsGM-vsyaoChvZxJr2jmpkYtw22ui0ZqqyfgLeO8CYZ5QHWIithtcAbMN1xuOD91cWMKHPmkUw"



	// Create a new request using http
	req, err := http.NewRequest("GET", url, nil)

	// add authorization header to the req
	req.Header.Add("Authorization", bearer)

	// Send req using http Client
	client = &http.Client{}
	resp, err = client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	log.Println(string([]byte(body)))

	// TODO: Fetch secrets from Safeguard
	return files, objectVersionMap, nil
}

func LoadX509KeyPair(certFile, keyFile string) (*x509.Certificate, *rsa.PrivateKey) {
	cf, e := ioutil.ReadFile(certFile)
	if e != nil {
		fmt.Println("cfload:", e.Error())
		os.Exit(1)
	}

	kf, e := ioutil.ReadFile(keyFile)
	if e != nil {
		fmt.Println("kfload:", e.Error())
		os.Exit(1)
	}
	cpb, cr := pem.Decode(cf)
	fmt.Println(string(cr))
	kpb, kr := pem.Decode(kf)
	fmt.Println(string(kr))
	crt, e := x509.ParseCertificate(cpb.Bytes)

	if e != nil {
		fmt.Println("parsex509:", e.Error())
		os.Exit(1)
	}
	key, e := x509.ParsePKCS1PrivateKey(kpb.Bytes)
	if e != nil {
		fmt.Println("parsekey:", e.Error())
		os.Exit(1)
	}
	return crt, key
}
