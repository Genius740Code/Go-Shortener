# multi stage build for smaller image size
FROM golang:1.21-alpine AS builder

# install git for go modules
RUN apk add --no-cache git

WORKDIR /app

# copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# copy source code
COPY . .

# build the binary - static linking for alpine
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

# final stage - minimal image
FROM alpine:latest

# add certificates for https requests
RUN apk --no-cache add ca-certificates

WORKDIR /root/

# copy binary from builder stage
COPY --from=builder /app/main .

# expose port
EXPOSE 8080

# run the binary
CMD ["./main"]