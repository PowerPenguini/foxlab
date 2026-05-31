export default {
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: 5174,
    host: '127.0.0.1',
    proxy: {
      '/api': 'http://127.0.0.1:8090',
    },
  },
};
