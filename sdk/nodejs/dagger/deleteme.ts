import { FSID } from "./client";

export class Yarn {
  script(context: any, source: FSID, runArgs: Array<string>): string {
    return "yarn-script";
  }

  Foo!: string;
}
