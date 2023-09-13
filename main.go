package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/cmd"
	"github.com/cert-manager/cert-manager/pkg/issuer/acme/dns/util"
	"go.uber.org/zap"
	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var GroupName = os.Getenv("GROUP_NAME")
var zapLogger, _ = zap.NewProduction()

func main() {
	if GroupName == "" {
		panic("GROUP_NAME must be specified")
	}

	cmd.RunWebhookServer(GroupName,
		&desechttpDNSProviderSolver{},
	)
}

type desechttpDNSProviderSolver struct {
	client *kubernetes.Clientset
}

type desechttpDNSProviderConfig struct {
	ApiUrl       string `json:"apiUrl"`
	DomainName   string `json:"domainName"`
	SecretRef    string `json:"secretName"`
	SecretKeyRef string `json:"secretKeyName"`
}

type Config struct {
	ApiKey, DomainName, ApiUrl string
}

type RRSet struct {
	Name    string     `json:"name,omitempty"`
	Domain  string     `json:"domain,omitempty"`
	SubName string     `json:"subname,omitempty"`
	Type    string     `json:"type,omitempty"`
	Records []string   `json:"records"`
	TTL     int        `json:"ttl,omitempty"`
	Created *time.Time `json:"created,omitempty"`
}

func (c *desechttpDNSProviderSolver) Name() string {
	return "desec-http"
}

func (c *desechttpDNSProviderSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	slogger := zapLogger.Sugar()
	slogger.Infof("call function Present: namespace=%s, zone=%s, fqdn=%s", ch.ResourceNamespace, ch.ResolvedZone, ch.ResolvedFQDN)

	config, err := clientConfig(c, ch)
	if err != nil {
		return fmt.Errorf("unable to get secret `%s`; %v", ch.ResourceNamespace, err)
	}

	addTxtRecord(config, ch)
	slogger.Infof("Presented txt record %v", ch.ResolvedFQDN)
	return nil
}

func (c *desechttpDNSProviderSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	slogger := zapLogger.Sugar()
	slogger.Infof("call function CleanUp: namespace=%s, zone=%s, fqdn=%s", ch.ResourceNamespace, ch.ResolvedZone, ch.ResolvedFQDN)

	config, err := clientConfig(c, ch)
	if err != nil {
		return fmt.Errorf("unable to get secret `%s`; %v", ch.ResourceNamespace, err)
	}

	removeTxtRecord(config, ch)
	slogger.Infof("Cleaned up txt record %v", ch.ResolvedFQDN)
	return nil
}

func (c *desechttpDNSProviderSolver) Initialize(kubeClientConfig *rest.Config, stopCh <-chan struct{}) error {
	slogger := zapLogger.Sugar()

	k8sClient, err := kubernetes.NewForConfig(kubeClientConfig)
	slogger.Infof("Input variable stopCh is %d length", len(stopCh))
	if err != nil {
		return err
	}

	c.client = k8sClient
	return nil
}

// Helpers -------------------------------------------------------------------
func addTxtRecord(config Config, ch *v1alpha1.ChallengeRequest) {
	/*
		Get TXT RRSet for subname
		RRSet exists?
			No
				POST: Create RRSet for subname with TXT record of ch.Key
			Yes
				Grab existing records and add ch.Key
				PUT: Update RRSet TXT records with new array

	*/
	
	var url string
	var content string
	var contentArr []string
	var httpMethod string
	var records *RRSet

	slogger := zapLogger.Sugar()

	// Get the subdomain portion of fqdn
	fqdn := util.UnFqdn(ch.ResolvedFQDN)
	subName := fqdn[:len(fqdn)-len(config.DomainName)-1]

	records, err := getTxtRecords(config, ch)
	if err != nil {
		slogger.Error(err)
	}

	// no TXT record exists (GET returned 404), so we will create one
	if records == nil && err == nil {
		slogger.Infof("====== getTxtRecords: 404 (no TXT found, now creating) ======")
		
		content = "\\\"" + ch.Key + "\\\""
		httpMethod = "POST"
		url = fmt.Sprintf("%s/domains/%s/rrsets/", config.ApiUrl, config.DomainName)
	}

	// at least one TXT record exists (GET returned 200), so we will append our key as a new record
	if records != nil && err == nil {
		slogger.Infof("====== getTxtRecords: 200 (TXT record found, now updating) ======")
		
		// deSEC returns TXT values quoted, so we have to remove leading and trailing double-quotes for each value one at a time
		for _, x := range records.Records {
			contentArr = append(contentArr, strings.Trim(x,"\""))
		}

		contentArr = append(contentArr, ch.Key)

		content = "\\\"" + strings.Join(contentArr, "\\\"\",\"\\\"") + "\\\""
		httpMethod = "PUT"
		url = fmt.Sprintf("%s/domains/%s/rrsets/%s.../TXT/", config.ApiUrl, config.DomainName, subName)
	}

	var jsonStr = fmt.Sprintf(`
		{
			"subname": "%s",
			"type": "TXT",
			"ttl": 3600,
			"records": ["%s"]
		}`, subName, content)

	slogger.Infof("JSON payload is: %s", string(jsonStr))

	add, _, err := callDnsApi(url, httpMethod, bytes.NewBuffer([]byte(jsonStr)), config)
	if err != nil {
		slogger.Error(err)
	}

	slogger.Infof("Added TXT record result: %s", string(add))
}

func removeTxtRecord(config Config, ch *v1alpha1.ChallengeRequest) {
	/*
		Get TXT RRSet for subname
		RRSet exists?
			No
				return nil (silent pass - do nothing)
			Yes
				Calculate existing records and remove ch.Key from array
				Array is now empty?
				Yes
					DELETE: subname RRSet
				No 
					PUT: Update RRSet TXT records with new array

	*/

	var url string
	var content string
	var contentArr []string
	var httpMethod string
	var records *RRSet

	slogger := zapLogger.Sugar()

	// Get the subdomain portion of fqdn
	fqdn := util.UnFqdn(ch.ResolvedFQDN)
	subName := fqdn[:len(fqdn)-len(config.DomainName)-1]

	records, err := getTxtRecords(config, ch)
	if err != nil {
		slogger.Error(err)
	}

	// no TXT record exists (GET returned 404), so we silently skip execution (nothing to do)
	if records == nil && err == nil {
		slogger.Infof("====== getTxtRecords: 404 (no TXT found, skipping) ======")
		
		content = ""
		httpMethod = ""
		url = ""
	}

	// at least one TXT record exists (GET returned 200), so we will scan for our key and remove it (or delete the entire record)
	if records != nil && err == nil {
		slogger.Infof("====== getTxtRecords: 200 (TXT record found) ======")

		// deSEC returns TXT values quoted, so we have to remove leading and trailing double-quotes for each value one at a time
		for _, x := range records.Records {
			contentArr = append(contentArr, strings.Trim(x,"\""))
		}

		var contentArrAmend []string
		// Create a new records slice containing all records except for the one to be deleted
		for _, r := range contentArr {
			if r != ch.Key {
				contentArrAmend  = append(contentArrAmend , r)
			}
		}

		if len(contentArrAmend) >= 1 {

			slogger.Infof("====== removeTxtRecord: Updating records ======")

			content = "\"\\\"" + strings.Join(contentArrAmend, "\\\"\",\"\\\"") + "\\\"\""
			httpMethod = "PUT"

		} else {

			slogger.Infof("====== removeTxtRecord: Deleting TXT for subname ======")

			content = ""
			httpMethod = "DELETE"

		}

		url = fmt.Sprintf("%s/domains/%s/rrsets/%s.../TXT/", config.ApiUrl, config.DomainName, subName)

	}

	if (httpMethod != "") && (url != "") {

		var jsonStr = fmt.Sprintf(`
		{
			"subname": "%s",
			"type": "TXT",
			"ttl": 3600,
			"records": [%s]
		}`, subName, content)

		slogger.Infof("JSON payload is: %s", string(jsonStr))

		remove, _, err := callDnsApi(url, httpMethod, bytes.NewBuffer([]byte(jsonStr)), config)
		if err != nil {
			slogger.Error(err)
		}

		slogger.Infof("Removed TXT record result: %s", string(remove))

	}

}

// Config ------------------------------------------------------
func stringFromSecretData(secretData *map[string][]byte, key string) (string, error) {
	data, ok := (*secretData)[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret data", key)
	}

	return string(data), nil
}

func loadConfig(cfgJSON *extapi.JSON) (desechttpDNSProviderConfig, error) {
	cfg := desechttpDNSProviderConfig{}

	// handle the 'base case' where no configuration has been provided
	if cfgJSON == nil {
		return cfg, nil
	}

	if err := json.Unmarshal(cfgJSON.Raw, &cfg); err != nil {
		return cfg, fmt.Errorf("error decoding solver config: %v", err)
	}
	return cfg, nil
}

func clientConfig(c *desechttpDNSProviderSolver, ch *v1alpha1.ChallengeRequest) (Config, error) {
	var config Config

	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return config, err
	}

	config.ApiUrl = cfg.ApiUrl
	config.DomainName = cfg.DomainName
	secretName := cfg.SecretRef
	secretKeyName := cfg.SecretKeyRef

	sec, err := c.client.CoreV1().Secrets(ch.ResourceNamespace).Get(context.TODO(), secretName, metav1.GetOptions{})
	if err != nil {
		return config, fmt.Errorf("unable to get secret `%s/%s`; %v", secretName, ch.ResourceNamespace, err)
	}

	apiKey, err := stringFromSecretData(&sec.Data, secretKeyName)
	config.ApiKey = apiKey
	if err != nil {
		return config, fmt.Errorf("unable to get api-key from secret `%s/%s`; %v", secretName, ch.ResourceNamespace, err)
	}

	return config, nil
}

// REST Request ------------------------------------------------------
func callDnsApi(url string, method string, body io.Reader, config Config) ([]byte, int, error) {
	slogger := zapLogger.Sugar()

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return []byte{}, -1, fmt.Errorf("unable to execute request %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token "+config.ApiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, -1, err
	}

	defer func() {
		err := resp.Body.Close()
		if err != nil {
			slogger.Fatal(err)
		}
	}()

	respBody, _ := io.ReadAll(resp.Body)

	var statusOK bool

	if method == "GET" {
		statusOK = (resp.StatusCode >= 200 && resp.StatusCode < 300) || (resp.StatusCode == 404)
	} else {
		statusOK = resp.StatusCode >= 200 && resp.StatusCode < 300
	}
	
	if statusOK {
		return respBody, resp.StatusCode, nil
	}

	text := "Error in callDnsApi:" + resp.Status + " url: " + url + " method: " + method + "body: " + string(respBody)
	slogger.Error(text)
	return nil, -1, errors.New(text)
}

func getTxtRecords(config Config, ch *v1alpha1.ChallengeRequest) (*RRSet, error) {
	
	slogger := zapLogger.Sugar()

	// Get the subdomain portion of fqdn
	fqdn := util.UnFqdn(ch.ResolvedFQDN)
	subName := fqdn[:len(fqdn)-len(config.DomainName)-1]
	
	url := fmt.Sprintf("%s/domains/%s/rrsets/%s.../TXT/", config.ApiUrl, config.DomainName, subName)

	var jsonStr = fmt.Sprintf(`{}`)

	get, statusCode, err := callDnsApi(url, "GET", bytes.NewBuffer([]byte(jsonStr)), config)
	if err != nil {
		return nil, fmt.Errorf("callDnsApi failed: %w", err)
	}
	
	var rrset RRSet

	if statusCode == 200 {
		
 		err := json.Unmarshal(get, &rrset)
		if err == nil {
			return &rrset, nil
		}
	}

	if statusCode == 404 {
		return nil, nil
	}

	text := "Error in getTxtRecords:" + fmt.Sprint(statusCode) + " url: " + url + "body: " + string(get)
	slogger.Error(text)
	return nil, errors.New(text)

}