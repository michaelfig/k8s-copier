# build stage
FROM golang:alpine AS build-env
RUN apk add git gcc
ADD . /src
RUN cd /src && go build -o k8s-copier

# final stage
FROM alpine
WORKDIR /app
COPY --from=build-env /src/k8s-copier /app/
CMD ./k8s-copier
