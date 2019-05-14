# build stage
FROM golang:alpine AS build-env
RUN apk add git gcc

WORKDIR $GOPATH/src/github.com/michaelfig/k8s-copier
COPY . .

# Download all the dependencies
RUN GO111MODULE=on go mod vendor

# FIXME: Edit the non-namespaced dynamicinformer
RUN sed -i -e 's/namespace:.*,/namespace: namespace,/' \
    vendor/k8s.io/client-go/dynamic/dynamicinformer/informer.go

# Install the binaries
RUN go install -v ./cmd/k8s-copier

WORKDIR /app
RUN cp $GOPATH/bin/k8s-copier /app/

# final stage
FROM alpine

LABEL maintainer="Michael FIG <michael+k8s-copier@fig.org>"

WORKDIR /app
COPY --from=build-env /app/k8s-copier /bin/k8s-copier

USER nobody
ENTRYPOINT ["/bin/k8s-copier"]
