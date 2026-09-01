{
  description = "huginn — attach to live Claude Code, Codex, and Grok Build sessions";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    zmqcat.url = "git+ssh://git@github.com/pyrex41/zmqcat";
    zmqcat.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = { self, nixpkgs, zmqcat }:
    let
      systems = [ "aarch64-darwin" "x86_64-darwin" "aarch64-linux" "x86_64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      # Aliased because `packages` re-exports an attribute named zmqcat: inside
      # a rec set that name would resolve to itself, not to the input.
      zmqcatFlake = zmqcat;
      zmqcatFor = pkgs: zmqcatFlake.packages.${pkgs.stdenv.hostPlatform.system}.zmqcat;
    in
    {
      overlays.default = final: prev: {
        huginn = final.callPackage ./nix/package.nix { };
        zmqcat = zmqcatFor final;
      };

      packages = forAll (pkgs: rec {
        huginn = pkgs.callPackage ./nix/package.nix { };
        # Re-exported so one flake input gets you both halves.
        zmqcat = zmqcatFor pkgs;
        default = huginn;
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.gopls (zmqcatFor pkgs) ];
        };
      });

      # One import per role.
      #   homeManagerModules.default — the sidecar, as the user who owns the sessions
      #   nixosModules.default       — sidecar (with an explicit user) and the MCP endpoint
      #   darwinModules.default      — the MCP endpoint
      # Each also pulls in the matching zmqcat module, so `services.zmqcat`
      # is configurable from the same import.
      homeManagerModules.default = import ./nix/home-module.nix self;
      nixosModules.default = {
        imports = [
          (import ./nix/nixos-module.nix self)
          zmqcatFlake.nixosModules.default
        ];
      };
      darwinModules.default = {
        imports = [
          (import ./nix/darwin-module.nix self)
          zmqcatFlake.darwinModules.default
        ];
      };

      formatter = forAll (pkgs: pkgs.nixpkgs-fmt);
    };
}
