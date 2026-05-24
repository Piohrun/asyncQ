const CopyWebpackPlugin = require('copy-webpack-plugin');
const fs = require('fs');
const path = require('path');
const ReplaceInFileWebpackPlugin = require('replace-in-file-webpack-plugin');
const TerserPlugin = require('terser-webpack-plugin');

const root = path.resolve(__dirname, '../..');
const sourceDir = path.join(root, 'src');
const distDir = path.join(root, 'dist');
const pkg = require(path.join(root, 'package.json'));
const pluginJson = require(path.join(sourceDir, 'plugin.json'));
const readmePath = fs.existsSync(path.join(sourceDir, 'README.md'))
  ? path.join(sourceDir, 'README.md')
  : path.join(root, 'README.md');

const externals = [
  { 'amd-module': 'module' },
  'angular',
  'jquery',
  'lodash',
  'moment',
  'react',
  'react-dom',
  'react/jsx-runtime',
  'rxjs',
  /^@grafana\/data/i,
  /^@grafana\/runtime/i,
  /^@grafana\/ui/i,
  ({ request }, callback) => {
    const prefix = 'grafana/';
    if (request && request.indexOf(prefix) === 0) {
      return callback(undefined, request.slice(prefix.length));
    }
    callback();
  },
];

module.exports = (_env, argv) => {
  const production = argv.mode === 'production';

  return {
    context: sourceDir,
    devtool: production ? 'source-map' : 'eval-source-map',
    entry: {
      module: path.join(sourceDir, 'module.ts'),
    },
    externals,
    mode: production ? 'production' : 'development',
    module: {
      rules: [
        {
          exclude: /node_modules/,
          test: /\.[tj]sx?$/,
          use: {
            loader: 'swc-loader',
            options: {
              jsc: {
                baseUrl: sourceDir,
                parser: {
                  decorators: false,
                  dynamicImport: true,
                  syntax: 'typescript',
                  tsx: true,
                },
                target: 'es2018',
              },
            },
          },
        },
        {
          test: /\.css$/,
          use: ['style-loader', 'css-loader'],
        },
        {
          test: /\.s[ac]ss$/,
          use: ['style-loader', 'css-loader', 'sass-loader'],
        },
        {
          test: /\.(png|jpe?g|gif|svg|woff2?|eot|ttf|otf)$/,
          type: 'asset/resource',
          generator: {
            filename: production ? '[hash][ext]' : '[file]',
          },
        },
      ],
    },
    optimization: {
      minimize: production,
      minimizer: [new TerserPlugin({ extractComments: false })],
    },
    output: {
      clean: {
        keep: /^(.*?_(amd64|arm64|arm)(\.exe)?|go_plugin_build_manifest)$/,
      },
      filename: '[name].js',
      library: {
        type: 'amd',
      },
      path: distDir,
      publicPath: `public/plugins/${pluginJson.id}/`,
      uniqueName: pluginJson.id,
    },
    plugins: [
      new CopyWebpackPlugin({
        patterns: [
          { from: path.join(sourceDir, 'plugin.json'), to: 'plugin.json' },
          { from: readmePath, to: 'README.md' },
          { from: path.join(root, 'LICENSE'), to: 'LICENSE', toType: 'file', noErrorOnMissing: true },
          { from: path.join(root, 'CHANGELOG.md'), to: 'CHANGELOG.md', noErrorOnMissing: true },
          { from: path.join(sourceDir, 'img'), to: 'img', noErrorOnMissing: true },
        ],
      }),
      new ReplaceInFileWebpackPlugin([
        {
          dir: distDir,
          test: [/plugin\.json$/, /README\.md$/],
          rules: [
            {
              search: /%VERSION%/g,
              replace: pkg.version,
            },
            {
              search: /%TODAY%/g,
              replace: new Date().toISOString().substring(0, 10),
            },
            {
              search: /%PLUGIN_ID%/g,
              replace: pluginJson.id,
            },
          ],
        },
      ]),
    ],
    resolve: {
      extensions: ['.js', '.jsx', '.ts', '.tsx'],
      modules: [sourceDir, 'node_modules'],
    },
  };
};
