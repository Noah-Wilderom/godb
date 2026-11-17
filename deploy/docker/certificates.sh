#!/bin/sh
set -eu
umask 077

group=11111111-1111-4111-8111-111111111111

if [ ! -e /ca/ca.key ] && [ ! -e /ca/ca.crt ]; then
    openssl genpkey -algorithm ED25519 -out /ca/ca.key
    openssl req -new -x509 -key /ca/ca.key -out /ca/ca.crt -days 3650 \
        -subj '/CN=godb-compose-local-ca' \
        -addext 'basicConstraints=critical,CA:TRUE' \
        -addext 'keyUsage=critical,keyCertSign,cRLSign'
fi

test -s /ca/ca.key && test -s /ca/ca.crt
openssl x509 -in /ca/ca.crt -checkend 86400 -noout

for id in 1 2 3; do
    directory="/certs/node$id"
    if [ ! -e "$directory/member.key" ] && [ ! -e "$directory/member.crt" ]; then
        openssl genpkey -algorithm ED25519 -out "$directory/member.key"
        openssl req -new -key "$directory/member.key" -out /tmp/member.csr \
            -subj "/CN=godb-compose-node$id"
        cat > /tmp/member.ext <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth,clientAuth
subjectAltName=DNS:node$id,URI:spiffe://godb/$group/node/$id
EOF
        openssl x509 -req -in /tmp/member.csr -CA /ca/ca.crt -CAkey /ca/ca.key \
            -set_serial "$id" -out "$directory/member.crt" -days 365 \
            -extfile /tmp/member.ext
    fi

    test -s "$directory/member.key" && test -s "$directory/member.crt"
    openssl verify -CAfile /ca/ca.crt -verify_hostname "node$id" "$directory/member.crt"
    openssl x509 -in "$directory/member.crt" -checkend 86400 -noout
    cp /ca/ca.crt "$directory/ca.crt"
    chown -R 10001:10001 "$directory"
    chmod 700 "$directory"
    chmod 400 "$directory/member.key"
    chmod 444 "$directory/member.crt" "$directory/ca.crt"
done
