{
  # Development shell for the CPA plugins. The plugins are cgo `c-shared`
  # libraries with a cgo SQLite dependency, so the shell has to supply a C
  # toolchain as well as Go — a bare `go` is not enough to build or even test
  # this repo.
  #
  # This shell is for iterating locally (`make test`, `make fmt`, `go build`).
  # It is deliberately *not* the release path: shippable .so artifacts are
  # built by the Dockerfile against bookworm/glibc, matching the runtime image.
  # Anything built in this shell links against the Nix glibc and is not
  # guaranteed to load in the CPA container.
  description = "CLIProxyAPI quota/auth/session-cache plugins — dev shell";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    { nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        # Pinned explicitly rather than tracking `pkgs.go`, so a nixpkgs bump
        # cannot silently move the toolchain under the repo. go.mod and the
        # Dockerfile builder are on 1.24; 1.25 is the oldest release still
        # packaged in nixpkgs-unstable and is compatible with that directive.
        go = pkgs.go_1_25;
      in
      {
        devShells.default = pkgs.mkShell {
          packages = [
            go
            pkgs.gcc # cgo: buildmode=c-shared and go-sqlite3
            pkgs.gnumake
            pkgs.git
            pkgs.gopls
            pkgs.gotools # goimports
            pkgs.go-tools # staticcheck
            pkgs.delve
            pkgs.sqlite # inspecting capture/health .db files by hand
            pkgs.gh
          ];

          env = {
            CGO_ENABLED = "1";
          };

          shellHook = ''
            echo "$(go version) · cgo enabled · make {fmt,test,build}"
          '';
        };

        formatter = pkgs.nixfmt-rfc-style;
      }
    );
}
