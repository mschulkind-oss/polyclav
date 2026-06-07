fn main() {
    // libsfizz: SFZ player. Probe pkg-config for paths only and emit
    // -lsfizz ourselves — the nixpkgs sfizz.pc has `Libs: -llibsfizz`
    // (doubled "lib" prefix bug), so cargo_metadata(true) would tell the
    // linker to look for `liblibsfizz.so` and fail.
    //
    // Also bake -Wl,-rpath=<sfizz/lib> so the resulting binary finds
    // libsfizz.so at runtime without requiring LD_LIBRARY_PATH.
    let probe = pkg_config::Config::new()
        .atleast_version("1.0.0")
        .cargo_metadata(false)
        .probe("sfizz")
        .expect("sfizz pkg-config probe failed — install sfizz");

    for path in &probe.link_paths {
        println!("cargo:rustc-link-search=native={}", path.display());
        println!("cargo:rustc-link-arg=-Wl,-rpath={}", path.display());
    }
    println!("cargo:rustc-link-lib=sfizz");
}
