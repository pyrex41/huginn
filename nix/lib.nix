{ lib }:

{
  mkSidecarArgs = cfg:
    [ "serve" "--bind" cfg.bind ]
    ++ cfg.extraArgs;

  # The token has to reach the process as an environment variable without
  # ever being a Nix store path, so every backend wraps the binary the same
  # way rather than each inventing its own.
  mkSidecarScript = { pkgs, lib, cfg }:
    pkgs.writeShellScript "huginn-sidecar" ''
      set -eu
      if [ ! -r ${lib.escapeShellArg (toString cfg.tokenFile)} ]; then
        echo "huginn: cannot read tokenFile ${toString cfg.tokenFile}" >&2
        exit 1
      fi
      HUGINN_TOKEN="$(cat ${lib.escapeShellArg (toString cfg.tokenFile)})"
      export HUGINN_TOKEN
      exec ${lib.getExe cfg.package} ${lib.escapeShellArgs (
        (import ./lib.nix { inherit lib; }).mkSidecarArgs cfg
      )}
    '';
}
