#!/bin/bash

mkdir -p certs

openssl req -x509 -newkey rsa:2048 -keyout certs/peer.key -out certs/peer.crt -days 365 -nodes -subj "/CN=localhost"

chmod 600 certs/peer.key
chmod 644 certs/peer.crt

echo "✅ Test certificates generated!"
echo "   Certificate: certs/peer.crt"
echo "   Private Key: certs/peer.key"
echo ""
echo "Usage:"
echo "   ./peer-cli -shareDir ./shared -tls -cert certs/peer.crt -key certs/peer.key start"
