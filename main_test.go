package main

import (
	"encoding/json"
	"math/rand"
	"os"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"github.com/cert-manager/cert-manager/test/acme/dns"
)

var (
	domain = os.Getenv("TEST_DOMAIN_NAME")
	apiKey = os.Getenv("TEST_SECRET")

	configFile         = "_test/data/config.json"
	secretYamlFilePath = "_test/data/cert-manager-webhook-desec-http-secret.yml"
	secretName         = "cert-manager-webhook-desec-http-secret"
	secretKeyName      = "api-key"
)

type SecretYaml struct {
	ApiVersion string `yaml:"apiVersion" json:"apiVersion"`
	Kind       string `yaml:"kind,omitempty" json:"kind,omitempty"`
	SecretType string `yaml:"type" json:"type"`
	Metadata   struct {
		Name string `yaml:"name"`
	}
	Data struct {
		ApiKey string `yaml:"api-key"`
	}
}

func TestRunsSuite(t *testing.T) {
	slogger := zapLogger.Sugar()

	secretYaml := SecretYaml{}
	secretYaml.ApiVersion = "v1"
	secretYaml.Kind = "Secret"
	secretYaml.SecretType = "Opaque"
	secretYaml.Metadata.Name = secretName
	secretYaml.Data.ApiKey = apiKey

	secretYamlFile, err := yaml.Marshal(&secretYaml)
	if err != nil {
		slogger.Error(err)
	}
	_ = os.WriteFile(secretYamlFilePath, secretYamlFile, 0644)

	providerConfig := desechttpDNSProviderConfig{
		"https://desec.io/api/v1",
		domain,
		secretName,
		secretKeyName,
	}
	file, _ := json.MarshalIndent(providerConfig, "", " ")
	_ = os.WriteFile(configFile, file, 0644)

	// resolvedFQDN must end with a '.'
	if domain[len(domain)-1:] != "." {
		domain = domain + "."
	}

	pollTime, _ := time.ParseDuration("15s")
	timeOut, _ := time.ParseDuration("5m")

	fixture := dns.NewFixture(&desechttpDNSProviderSolver{},
		dns.SetDNSName(domain),
		dns.SetResolvedZone(domain),
		dns.SetResolvedFQDN(GetRandomString(8)+"."+domain),
		// Increase the poll interval to 15s
		dns.SetPollInterval(pollTime),
		// Increase the limit from 2 min to 5 min as we need more time for the propagation of the TXT Record
		dns.SetPropagationLimit(timeOut),
		dns.SetAllowAmbientCredentials(false),
		dns.SetManifestPath("_test/data"),
		dns.SetStrict(true),
	)

	fixture.RunConformance(t)

	_ = os.Remove(configFile)
	_ = os.Remove(secretYamlFilePath)
}

func GetRandomString(n int) string {
	letters := []rune("abcdefghijklmnopqrstuvwxyz")

	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}

	return string(b)
}
