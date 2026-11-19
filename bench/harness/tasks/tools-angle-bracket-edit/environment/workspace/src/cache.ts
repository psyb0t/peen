export class Cache<T> {
  private items: Map<string, T> = new Map<string, T>();

  get(key: string): T | undefined {
    return this.items.get(key);
  }

  has(key: string): boolean {
    return this.items.has(key) && this.items.get(key) !== undefined;
  }

  keep(key: string, value: T): void {
    if (key.length > 0 && value !== undefined) {
      this.items.set(key, value);
    }
  }
}
