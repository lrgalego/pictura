{
  description = "story-time — dev environment";

  inputs = {
    # unstable, because go.mod requires >= 1.26.1 and stable channels lag.
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      # Intel Macs are deliberately absent: nothing here is built or tested on
      # one, and declaring a system is a claim of support. Both Linux systems
      # stay — x86_64 is what the VM and any CI runner run.
      systems = [
        "aarch64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});

      # `templ` here is a two-line shim onto `go tool templ`, not the real CLI.
      #
      # The version is pinned by the `tool` directive in go.mod so it can never
      # drift from the library the generated code links against. Leaving templ
      # out of the shell entirely — the previous approach — pinned `make
      # generate` but nothing else: asdf is active globally with its shim early
      # in PATH, so typing `templ generate` by hand silently ran whatever
      # version asdf had. That is not a harmless mismatch. An older CLI happily
      # regenerates every _templ.go and *downgrades* it — v0.3.1001 rewrites
      # v0.3.1020's `ResolveAttributeValue` back to `JoinStringErrs` across the
      # whole tree, a diff that looks like noise and reverts a real fix.
      #
      # So shadow the stray binary rather than yield PATH to it: whichever way
      # templ is invoked inside the shell, the pinned one runs.
      templShim =
        pkgs:
        pkgs.writeShellScriptBin "templ" ''
          # Resolves through go.mod, so it must run inside the module.
          exec go tool templ "$@"
        '';
    in
    {
      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages =
            [
              pkgs.go_1_26
              (templShim pkgs)
              pkgs.hcloud
              pkgs.cloudflared
              pkgs.git
              pkgs.gnumake
              # For WAL-safe snapshots (VACUUM INTO) and ad-hoc queries. macOS
              # ships a sqlite3, but pinning it here is what makes the documented
              # backup command work identically on Linux.
              pkgs.sqlite
            ]
            # Nix cannot supply a Linux daemon on macOS, so the client is only
            # useful on Linux; macOS gets it from Docker Desktop or colima.
            ++ pkgs.lib.optional pkgs.stdenv.isLinux pkgs.docker-client;

          # Kept cheap on purpose — this runs on every shell entry.
          shellHook = ''
            echo "story-time dev shell · $(go version | cut -d' ' -f3)"
            echo "  make doctor  — verify the toolchain"
          '';
        };
      });
    };
}
