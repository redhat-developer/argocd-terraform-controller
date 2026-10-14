import path from 'path';

// Argo CD loads every /tmp/extensions/**/extension*.js file and provides
// React on window.React, so React must not be bundled.
export default (_env, argv) => ({
  entry: './src/index.tsx',
  mode: argv.mode ?? 'production',
  devtool: argv.mode === 'development' ? 'inline-source-map' : false,
  output: {
    filename: 'extension-terraform.js',
    path: path.resolve(process.cwd(), 'dist'),
    library: { type: 'window' },
  },
  resolve: { extensions: ['.ts', '.tsx', '.js'] },
  externals: { react: 'React', 'react-dom': 'ReactDOM' },
  module: {
    rules: [
      {
        test: /\.tsx?$/,
        exclude: /node_modules/,
        // tsconfig sets noEmit for `tsc` type checks; the loader must emit.
        use: { loader: 'ts-loader', options: { compilerOptions: { noEmit: false } } },
      },
      { test: /\.css$/, use: ['style-loader', 'css-loader'] },
    ],
  },
  performance: { hints: false },
});
