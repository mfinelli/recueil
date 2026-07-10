[private]
default:
  @just --list

fmt:
  pnpm run fmt
  tofu fmt -recursive
