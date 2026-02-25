#!/bin/sh
# Generate self-signed ECDSA P-256 certificates for TiKV TLS.
# This script runs in the cert-gen init container.

set -e

CERT_DIR=/certs

# Skip if certs already exist (idempotent)
if [ -f "$CERT_DIR/ca.pem" ]; then
    echo "Certificates already exist, skipping generation"
    exit 0
fi

# Install openssl
apk add --no-cache openssl >/dev/null 2>&1

# Generate CA key and cert
openssl ecparam -genkey -name prime256v1 -noout -out "$CERT_DIR/ca-key.pem"
openssl req -new -x509 -key "$CERT_DIR/ca-key.pem" -out "$CERT_DIR/ca.pem" \
    -days 1 -subj "/O=go-quicraft/CN=tikv-bench-ca" \
    -addext "basicConstraints=critical,CA:TRUE"

# Generate server key and CSR
openssl ecparam -genkey -name prime256v1 -noout -out "$CERT_DIR/server-key.pem"
openssl req -new -key "$CERT_DIR/server-key.pem" -out "$CERT_DIR/server.csr" \
    -subj "/O=go-quicraft/CN=tikv-bench-server"

# Sign server cert with SANs
cat > "$CERT_DIR/server-ext.cnf" <<EOF
[v3_ext]
subjectAltName = DNS:localhost,DNS:pd,DNS:tikv,DNS:tikv1,DNS:tikv2,DNS:tikv3,DNS:pd1,DNS:pd2,DNS:pd3,IP:127.0.0.1
keyUsage = digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth,clientAuth
EOF

openssl x509 -req -in "$CERT_DIR/server.csr" -CA "$CERT_DIR/ca.pem" -CAkey "$CERT_DIR/ca-key.pem" \
    -CAcreateserial -out "$CERT_DIR/server.pem" -days 1 \
    -extfile "$CERT_DIR/server-ext.cnf" -extensions v3_ext

# Generate client key and cert (for rawkv client)
openssl ecparam -genkey -name prime256v1 -noout -out "$CERT_DIR/client-key.pem"
openssl req -new -key "$CERT_DIR/client-key.pem" -out "$CERT_DIR/client.csr" \
    -subj "/O=go-quicraft/CN=tikv-bench-client"

cat > "$CERT_DIR/client-ext.cnf" <<EOF
[v3_ext]
keyUsage = digitalSignature,keyEncipherment
extendedKeyUsage = clientAuth
EOF

openssl x509 -req -in "$CERT_DIR/client.csr" -CA "$CERT_DIR/ca.pem" -CAkey "$CERT_DIR/ca-key.pem" \
    -CAcreateserial -out "$CERT_DIR/client.pem" -days 1 \
    -extfile "$CERT_DIR/client-ext.cnf" -extensions v3_ext

# Cleanup CSRs and config
rm -f "$CERT_DIR"/*.csr "$CERT_DIR"/*.cnf "$CERT_DIR"/*.srl

# Set permissions
chmod 644 "$CERT_DIR"/*.pem
chmod 600 "$CERT_DIR"/*-key.pem

echo "TLS certificates generated successfully"
ls -la "$CERT_DIR"
