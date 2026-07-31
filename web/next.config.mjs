/** @type {import('next').NextConfig} */
const nextConfig = {
  // standalone: самодостаточный server.js для тонкого Docker-образа (deploy/docker-compose.yml)
  output: "standalone",
};

export default nextConfig;
