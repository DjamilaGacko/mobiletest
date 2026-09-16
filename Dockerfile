FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build_base
RUN apk add --no-cache git gcc ca-certificates libc-dev
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY ./ ./

ENV CGO_ENABLED=0
ARG TARGETOS TARGETARCH
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-w -s" -trimpath -buildvcs=false -o speedtest .

FROM scratch
WORKDIR /app
COPY --from=build_base /build/speedtest ./
COPY settings.toml ./
COPY web/ ./web/
# Seuils ARCEP (profils D1-D4, zones) : fichier de config modifiable sans
# recompilation - cf. config/seuils.json et seuil.md a la racine du projet.
COPY config/ ./config/

EXPOSE 8989

# Les assets web sont embarques dans le binaire (go:embed) ; avec
# assets_path="" de settings.toml, ce sont eux qui sont servis.
# (L'ancien `--asset-path` faisait planter le conteneur au demarrage :
# ce flag n'existe pas dans main.go, seul -c est defini.)
CMD ["./speedtest"]
