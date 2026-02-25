#!/bin/bash
# Generate TLS certificates for qrdb integration testing

set -e

CERT_DIR="${CERT_DIR:-/etc/qrdb/certs}"
DAYS_VALID="${DAYS_VALID:-365}"

echo "Generating test TLS certificates in ${CERT_DIR}..."

mkdir -p "${CERT_DIR}"
cd "${CERT_DIR}"

# Generate CA private key
openssl ecparam -genkey -name prime256v1 -out ca.key 2>/dev/null

# Generate CA certificate
openssl req -x509 -new -nodes -key ca.key -sha256 -days ${DAYS_VALID} \
    -out ca.crt \
    -subj "/C=US/ST=Test/L=Test/O=QRDB Test CA/CN=QRDB Test CA" 2>/dev/null

# Generate server private key
openssl ecparam -genkey -name prime256v1 -out server.key 2>/dev/null

# Create server CSR config with SANs
cat > server.cnf << EOF
[req]
default_bits = 256
prompt = no
default_md = sha256
req_extensions = req_ext
distinguished_name = dn

[dn]
C = US
ST = Test
L = Test
O = QRDB Test Server
CN = qrdbd

[req_ext]
subjectAltName = @alt_names

[alt_names]
DNS.1 = localhost
DNS.2 = qrdbd
DNS.3 = qrdb-integration-server
IP.1 = 127.0.0.1
IP.2 = ::1
EOF

# Generate server CSR
openssl req -new -key server.key -out server.csr -config server.cnf 2>/dev/null

# Create extension config for server cert
cat > server_ext.cnf << EOF
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = @alt_names

[alt_names]
DNS.1 = localhost
DNS.2 = qrdbd
DNS.3 = qrdb-integration-server
IP.1 = 127.0.0.1
IP.2 = ::1
EOF

# Sign server certificate with CA
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
    -out server.crt -days ${DAYS_VALID} -sha256 \
    -extfile server_ext.cnf 2>/dev/null

# Clean up temp files
rm -f server.csr server.cnf server_ext.cnf ca.srl

# Set permissions
chmod 644 ca.crt server.crt
chmod 600 ca.key server.key

echo "TLS certificates generated successfully:"
echo "  CA Certificate: ${CERT_DIR}/ca.crt"
echo "  Server Certificate: ${CERT_DIR}/server.crt"
echo "  Server Key: ${CERT_DIR}/server.key"

# Verify the certificate
echo ""
echo "Server certificate details:"
openssl x509 -in server.crt -noout -subject -issuer -dates 2>/dev/null
