package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io/ioutil"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"gitlab.com/gitlab-org/labkit/correlation"
	"gitlab.com/gitlab-org/labkit/tracing"
	"gitlab.com/gitlab-org/labkit/log"
)

const (
	socketBaseUrl             = "http://unix"
	unixSocketProtocol        = "http+unix://"
	httpProtocol              = "http://"
	httpsProtocol             = "https://"
	defaultReadTimeoutSeconds = 300
)

type HttpClient struct {
	*http.Client
	Host string
}

type httpClientCfg struct {
	keyPath, certPath string
	caFile, caPath    string
}

func (hcc httpClientCfg) HaveCertAndKey() bool { return hcc.keyPath != "" && hcc.certPath != "" }

// HTTPClientOpt provides options for configuring an HttpClient
type HTTPClientOpt func(*httpClientCfg)

// WithClientCert will configure the HttpClient to provide client certificates
// when connecting to a server.
func WithClientCert(certPath, keyPath string) HTTPClientOpt {
	return func(hcc *httpClientCfg) {
		hcc.keyPath = keyPath
		hcc.certPath = certPath
	}
}

// Deprecated: use NewHTTPClientWithOpts - https://gitlab.com/gitlab-org/gitlab-shell/-/issues/484
func NewHTTPClient(gitlabURL, gitlabRelativeURLRoot, caFile, caPath string, selfSignedCert bool, readTimeoutSeconds uint64) *HttpClient {
	c, err := NewHTTPClientWithOpts(gitlabURL, gitlabRelativeURLRoot, caFile, caPath, selfSignedCert, readTimeoutSeconds, nil)
	if err != nil {
		log.WithError(err).Error("new http client with opts")
	}
	return c
}

// NewHTTPClientWithOpts builds an HTTP client using the provided options
func NewHTTPClientWithOpts(gitlabURL, gitlabRelativeURLRoot, caFile, caPath string, selfSignedCert bool, readTimeoutSeconds uint64, opts []HTTPClientOpt) (*HttpClient, error) {
	hcc := &httpClientCfg{
		caFile: caFile,
		caPath: caPath,
	}

	for _, opt := range opts {
		opt(hcc)
	}

	var transport *http.Transport
	var host string
	var err error
	if strings.HasPrefix(gitlabURL, unixSocketProtocol) {
		transport, host = buildSocketTransport(gitlabURL, gitlabRelativeURLRoot)
	} else if strings.HasPrefix(gitlabURL, httpProtocol) {
		transport, host = buildHttpTransport(gitlabURL)
	} else if strings.HasPrefix(gitlabURL, httpsProtocol) {
		transport, host, err = buildHttpsTransport(*hcc, selfSignedCert, gitlabURL)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("unknown GitLab URL prefix")
	}

	c := &http.Client{
		Transport: correlation.NewInstrumentedRoundTripper(tracing.NewRoundTripper(transport)),
		Timeout:   readTimeout(readTimeoutSeconds),
	}

	client := &HttpClient{Client: c, Host: host}

	return client, nil
}

func buildSocketTransport(gitlabURL, gitlabRelativeURLRoot string) (*http.Transport, string) {
	socketPath := strings.TrimPrefix(gitlabURL, unixSocketProtocol)

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := net.Dialer{}
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}

	host := socketBaseUrl
	gitlabRelativeURLRoot = strings.Trim(gitlabRelativeURLRoot, "/")
	if gitlabRelativeURLRoot != "" {
		host = host + "/" + gitlabRelativeURLRoot
	}

	return transport, host
}

func buildHttpsTransport(hcc httpClientCfg, selfSignedCert bool, gitlabURL string) (*http.Transport, string, error) {
	certPool, err := x509.SystemCertPool()

	if err != nil {
		certPool = x509.NewCertPool()
	}

	if hcc.caFile != "" {
		addCertToPool(certPool, hcc.caFile)
	}

	if hcc.caPath != "" {
		fis, _ := ioutil.ReadDir(hcc.caPath)
		for _, fi := range fis {
			if fi.IsDir() {
				continue
			}

			addCertToPool(certPool, filepath.Join(hcc.caPath, fi.Name()))
		}
	}
	tlsConfig := &tls.Config{
		RootCAs:            certPool,
		InsecureSkipVerify: selfSignedCert,
		MinVersion:         tls.VersionTLS12,
	}

	if hcc.HaveCertAndKey() {
		cert, err := tls.LoadX509KeyPair(hcc.certPath, hcc.keyPath)
		if err != nil {
			return nil, "", err
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
		tlsConfig.BuildNameToCertificate()
	}

	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
	}

	return transport, gitlabURL, err
}

func addCertToPool(certPool *x509.CertPool, fileName string) {
	cert, err := ioutil.ReadFile(fileName)
	if err == nil {
		certPool.AppendCertsFromPEM(cert)
	}
}

func buildHttpTransport(gitlabURL string) (*http.Transport, string) {
	return &http.Transport{}, gitlabURL
}

func readTimeout(timeoutSeconds uint64) time.Duration {
	if timeoutSeconds == 0 {
		timeoutSeconds = defaultReadTimeoutSeconds
	}

	return time.Duration(timeoutSeconds) * time.Second
}
