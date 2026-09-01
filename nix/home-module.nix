self:
{ config, lib, pkgs, ... }:

let
  cfg = config.services.huginn;
  hlib = import ./lib.nix { inherit lib; };
  script = hlib.mkSidecarScript { inherit pkgs lib cfg; };
in
{
  options.services.huginn = import ./sidecar-options.nix { inherit lib; } // {
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.huginn;
      defaultText = lib.literalExpression "huginn.packages.\${system}.huginn";
      description = "huginn package to run.";
    };
  };

  # The sidecar must run as the human whose sessions it attaches to: it
  # reads ~/.grok, ~/.claude, and ~/.codex. A root service would see none of
  # them, which is why home-manager is the primary way to run this.
  config = lib.mkIf cfg.enable {
    systemd.user.services.huginn = lib.mkIf pkgs.stdenv.hostPlatform.isLinux {
      Unit = {
        Description = "huginn session sidecar";
        After = [ "default.target" ];
      };
      Service = {
        ExecStart = "${script}";
        Restart = "always";
        RestartSec = 2;
      };
      Install.WantedBy = [ "default.target" ];
    };

    launchd.agents.huginn = lib.mkIf pkgs.stdenv.hostPlatform.isDarwin {
      enable = true;
      config = {
        ProgramArguments = [ "${script}" ];
        RunAtLoad = true;
        KeepAlive = true;
        StandardOutPath = "${config.home.homeDirectory}/Library/Logs/huginn.log";
        StandardErrorPath = "${config.home.homeDirectory}/Library/Logs/huginn.log";
      };
    };
  };
}
