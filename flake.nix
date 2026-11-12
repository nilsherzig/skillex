{
  description = "Link agent skills from local directories and pinned git repositories into agent homes";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      forAllSystems = nixpkgs.lib.genAttrs [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.buildGoModule {
            pname = "skillman";
            version = self.shortRev or "dirty";
            src = self;
            vendorHash = "sha256-f01JOwTLVtbLGf4a8QCjtbEjYDDlk1f1BaGx44dokGQ=";
            nativeBuildInputs = [ pkgs.makeWrapper ];
            nativeCheckInputs = [ pkgs.git ];
            postInstall = ''
              wrapProgram $out/bin/skillman --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.git ]}
            '';
            meta.mainProgram = "skillman";
          };
        }
      );

      devShells = forAllSystems (system: {
        default = nixpkgs.legacyPackages.${system}.mkShell {
          packages = with nixpkgs.legacyPackages.${system}; [
            go
            git
          ];
        };
      });
    };
}
