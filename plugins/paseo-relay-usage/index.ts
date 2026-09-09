import type { PluginContext } from '@getpaseo/plugin';
import { MainSurface } from './main.client';
import { getUsage } from './contracts';
import { readUsage } from './usage.server';
export default function contribute(plugin: PluginContext) {
  plugin.handle(getUsage, () => readUsage());
  plugin.addSurface('main', MainSurface);
  plugin.addSidebarItem({ id: 'relay-usage', title: '中转站用量', icon: 'Wallet', surface: 'main' });
  plugin.addCommandCenterItem({ id: 'open-usage', title: '查看中转站用量', icon: 'Wallet', context: 'global', onSelect: c => c.openSurface('main') });
  return () => {};
}
