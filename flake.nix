{
  description = "huginn — attach to live Claude Code, Codex, and Grok Build sessions";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-darwin" "x86_64-darwin" "aarch64-linux" "x86_64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      overlays.default = final: prev: {
        huginn = final.callPackage ./nix/package.nix { };
      };

      packages = forAll (pkgs: rec {
        huginn = pkgs.callPackage ./nix/package.nix { };
        default = huginn;
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.gopls ];
        };
      });

      homeManagerModules.default = import ./nix/home-module.nix self;
      nixosModules.default = import ./nix/nixos-module.nix self;

      formatter = forAll (pkgs: pkgs.nixpkgs-fmt);
    };
}
