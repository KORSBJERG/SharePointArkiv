package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const certName = "klient.cer"
const keyName = "klient-noegle.pem"

type credential struct {
	clientID    string
	tokenURL    string
	certificate *x509.Certificate
	key         *rsa.PrivateKey
	token       string
	expires     time.Time
}

func generateCertificate(dir string) error {
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "SharePoint Arkiv"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	cert, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyPath := filepath.Join(dir, keyName)
	certPath := filepath.Join(dir, certName)
	if err := writeNewFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		return err
	}
	if err := writeNewFile(certPath, cert, 0644); err != nil {
		os.Remove(keyPath)
		return err
	}
	return nil
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func loadCredential(cfg config, dir string) (*credential, error) {
	data, err := os.ReadFile(filepath.Join(dir, certName))
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(data)
	if err != nil {
		return nil, fmt.Errorf("læs klientcertifikatet: %w", err)
	}
	if now := time.Now(); now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return nil, errors.New("klientcertifikatet er uden for sin gyldighedsperiode")
	}
	data, err = os.ReadFile(filepath.Join(dir, keyName))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("den private nøgle skal være en RSA-nøgle i PEM-format")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("læs den private nøgle: %w", err)
	}
	publicKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || !key.PublicKey.Equal(publicKey) {
		return nil, errors.New("klientcertifikatet og den private nøgle passer ikke sammen")
	}
	return &credential{
		clientID:    cfg.ClientID,
		tokenURL:    "https://login.microsoftonline.com/" + cfg.TenantID + "/oauth2/v2.0/token",
		certificate: cert,
		key:         key,
	}, nil
}

func (c *credential) accessToken(ctx context.Context, client *http.Client) (string, error) {
	now := time.Now()
	if c.token != "" && now.Add(2*time.Minute).Before(c.expires) {
		return c.token, nil
	}
	thumbprint := sha256.Sum256(c.certificate.Raw)
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	encode := base64.RawURLEncoding.EncodeToString
	header, err := json.Marshal(map[string]string{"alg": "PS256", "typ": "JWT", "x5t#S256": encode(thumbprint[:])})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"aud": c.tokenURL, "iss": c.clientID, "sub": c.clientID, "jti": encode(id),
		"iat": now.Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	})
	if err != nil {
		return "", err
	}
	assertion := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(assertion))
	signature, err := rsa.SignPSS(rand.Reader, c.key, crypto.SHA256, digest[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		return "", err
	}
	form := url.Values{
		"client_id":             {c.clientID},
		"scope":                 {"https://graph.microsoft.com/.default"},
		"grant_type":            {"client_credentials"},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion + "." + encode(signature)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("kontakt Microsoft Entra: %w", err)
	}
	defer resp.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrorCodes  []int  `json:"error_codes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return "", fmt.Errorf("ugyldigt svar fra Microsoft Entra (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || token.AccessToken == "" || token.ExpiresIn <= 0 {
		return "", fmt.Errorf("Microsoft Entra afviste logon (HTTP %d, fejlkoder %v)", resp.StatusCode, token.ErrorCodes)
	}
	c.token = token.AccessToken
	c.expires = now.Add(time.Duration(token.ExpiresIn) * time.Second)
	return c.token, nil
}
