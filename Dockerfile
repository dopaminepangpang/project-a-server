FROM golang:alpine

WORKDIR /build

COPY go.mod go.sum ./

RUN go mod download

RUN go build -o /app main.go

EXPOSE 8080

CMD [ "/app" ]