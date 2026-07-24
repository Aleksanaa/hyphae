{
  description = "A coding agent";

  inputs = {
    flake-parts.url = "github:hercules-ci/flake-parts";
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
    systems.url = "github:nix-systems/default";
  };

  outputs =
    inputs:
    inputs.flake-parts.lib.mkFlake { inherit inputs; } {
      systems = import inputs.systems;

      imports = [ inputs.flake-parts.flakeModules.easyOverlay ];

      perSystem =
        { config, pkgs, ... }:
        {
          packages = rec {
            hyphae =
              with pkgs;
              buildGoModule {
                name = "hyphae";

                src = lib.cleanSource ./.;

                vendorHash = "sha256-IqobaSXC1i5D97jLUlYtFkO5TW0jjEOg2j+KS0E/FwE=";

                env.CGO_ENABLED = 0;

                ldflags = [
                  "-s"
                  "-w"
                ];
              };
            default = hyphae;
          };

          overlayAttrs = {
            inherit (config.packages) hyphae;
          };

          devShells.default = pkgs.mkShellNoCC {
            env.CGO_ENABLED = 0;
            packages = with pkgs; [
              go
              gopls
            ];
          };
        };
    };
}
