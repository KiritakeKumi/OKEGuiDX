"""Compatibility entry point for Windows x64 builds."""
from build_candidate import main

if __name__ == "__main__":
    main(default_target="win-x64")
