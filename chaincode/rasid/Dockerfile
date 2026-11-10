# RASID chaincode as a service, for DRUNIX (network/drunix/deploy.sh) or any
# Fabric 2.5 network that uses the ccaas builder. The binary is built on the
# host first, so the image needs no network access:
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o rasid-cc .
#   docker build -t rasid-cc .
FROM scratch
COPY rasid-cc /rasid-cc
EXPOSE 9999
ENV CHAINCODE_SERVER_ADDRESS=0.0.0.0:9999
ENTRYPOINT ["/rasid-cc"]
