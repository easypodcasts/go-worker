{ lib, buildGoPackage}:
let
  yorodm = {
    name = "Yoandy Rodriguez" ;
    email = "mr.dominus@gmail.com";
    github = "yorodm";
  };
in
buildGoPackage rec {
  name = "go-worker-${version}";
  version = "v.0.1.0";
  goPackagePath = "github.com/easypodcasts/go-worker";

  src = lib.cleanSource ./.;

  meta = {
    description = "Simple feed parser for Easypodcasts";
    homepage = https://github.com/easypodcasts/go-worker;
    maintainers = [ yorodm ];
  };

}
