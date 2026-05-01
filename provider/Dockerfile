FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 go build -o /out/approval-verifier .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/approval-verifier /approval-verifier
USER nonroot:nonroot
EXPOSE 8443
ENTRYPOINT ["/approval-verifier"]
