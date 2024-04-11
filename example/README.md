# Example setup

The example is using the deSec webhook, Traefik and cert-manager with Let's Encrypt

## Development

To replicate this example follow the steps described below.

### Pre-requisite

#### Create A record in deSec

This is an example:

`A nginx1 <Your server IP> 3600`

#### Install cert-manager and traefik

1. Install cert-manager following the official guide <https://cert-manager.io/docs/installation/> (recommended is to use Helm)
2. Install traefik <https://doc.traefik.io/traefik/getting-started/install-traefik/> (recommended is to use Helm)

### Create Secret for deSec

```sh
kubectl apply -f secret-desec.yaml
```

### Install the deSec Webhook via Helm

```sh
helm install desec-http oci://ghcr.io/irreleph4nt/charts/cert-manager-webhook-desec-http -f values.yaml -n cert-manager
```

### Validate webhook connection

Check if the connection is successfully made.

#### Clone the repo and `cd` into it

```sh
git clone git@github.com:irreleph4nt/cert-manager-webhook-desec-http.git
cd cert-manager-webhook-desec-http
```

#### Run the test script

```sh
TEST_DOMAIN_NAME=<domain name> TEST_SECRET=$(echo -n '<DESEC API TOKEN>' | base64) make test
```

- Expect Ok status to know that it is working

### Apply cluster issuer

```sh
kubectl apply -f cluster-issuer-acme-prod.yaml
```

In case you use staging instead:

```sh
kubectl apply -f cluster-issuer-acme-stag.yaml
```

### Create namespace for the testing app

```sh
kubectl apply -f nginx-ns.yaml
```

### Apply certificate for production

```sh
kubectl apply -f certificate-prod.yaml
```

In case you use staging instead:

```sh
kubectl apply -f certificate-stag.yaml
```

#### Check if the certificate is ready to use

```sh
kubectl describe certificaterequest -n nginx1
```

**Note:** This is should result in Events message
`Certificate fetched from issuer successfully`

### Deploy example app

```sh
kubectl apply -f nginx-test.yaml
```

#### Validate

1. Go to the browser and open <https://nginx1.example.com>
2. Check if the connection is secure
3. Check if the certificate is valid and issued from Let's Encrypt

You are all done!
