class Silo < Formula
  desc "Run AI coding assistants in containers/vms"
  homepage "https://github.com/leighmcculloch/silo"
  head "https://github.com/leighmcculloch/silo.git", branch: "main"
  license "Apache-2.0"

  depends_on "go" => :build
  depends_on :macos

  def install
    system "go", "build", "-o", bin/"silo", "."
  end
end
