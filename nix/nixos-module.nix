self:
{ config, lib, pkgs, ... }:

let
  cfg = config.services.huginn;
  hlib = import ./lib.nix { inherit lib; };
  system = pkgs.stdenv.hostPlatform.system;
  sidecarScript = hlib.mkSidecarScript { inherit pkgs lib; cfg = cfg; };
in
{
  options.services.huginn = import ./sidecar-options.nix { inherit lib; } // {
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${system}.huginn;
      defaultText = lib.literalExpression "huginn.packages.\${system}.huginn";
      description = "huginn package to run.";
    };
    user = lib.mkOption {
      type = lib.types.str;
      example = "reuben";
      description = ''
        The human whose coding sessions this attaches to. Required: the
        sidecar reads that user's ~/.grok, ~/.claude, and ~/.codex, so
        running it as root or a system user would find nothing.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [{
      assertion = cfg.user != "root";
      message = "services.huginn.user must be the human whose sessions it attaches to, not root.";
    }];
    systemd.services.huginn = {
      description = "huginn session sidecar";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      serviceConfig = {
        ExecStart = sidecarScript;
        Restart = "always";
        RestartSec = 2;
        User = cfg.user;
        NoNewPrivileges = true;
      };
    };
  };
}
