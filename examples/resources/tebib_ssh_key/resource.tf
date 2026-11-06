resource "tebib_ssh_key" "me" {
  name       = "me"
  public_key = file(pathexpand("~/.ssh/id_ed25519.pub"))
}
