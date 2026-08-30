# frozen_string_literal: true

# Formula for the session-finder CLI.
class SessionFinder < Formula
  desc "Search local AI sessions from multiple tools"
  homepage "https://github.com/BayInl/session-finder"
  version "0.1.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/BayInl/session-finder/releases/download/v#{version}/session-finder_#{version}_darwin_arm64.tar.gz"
      sha256 "0000000000000000000000000000000000000000000000000000000000000000" # TODO: replace after v0.1.0 release
    else
      url "https://github.com/BayInl/session-finder/releases/download/v#{version}/session-finder_#{version}_darwin_amd64.tar.gz"
      sha256 "1111111111111111111111111111111111111111111111111111111111111111" # TODO: replace after v0.1.0 release
    end
  end

  on_linux do
    if Hardware::CPU.intel?
      url "https://github.com/BayInl/session-finder/releases/download/v#{version}/session-finder_#{version}_linux_amd64.tar.gz"
      sha256 "2222222222222222222222222222222222222222222222222222222222222222" # TODO: replace after v0.1.0 release
    end
  end

  def install
    bin.install "session-finder"
  end

  test do
    system bin / "session-finder", "--help"
  end
end
